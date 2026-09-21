// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package storageclass manages the admin-registered directories where Miabi creates volumes. A
// class inverts the host-path trust model: the tenant names a class, the platform derives the path,
// so an unprivileged workspace can use operator storage without ever supplying a host path.
package storageclass

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// Errors the handlers map to 4xx responses.
var (
	// ErrImmutable is returned when an update tries to change a class's name or path. Both are
	// dereferenced rather than displayed — volumes and GitOps manifests point at the name, and
	// volumes already exist under the path — so a rewrite would break references, or silently
	// point live volumes at a directory their data is not in.
	ErrImmutable = errors.New("a storage class name and path cannot be changed after registration; register a new class instead")
	// ErrBuiltin is returned when an admin tries to delete or re-path the seeded default class.
	ErrBuiltin = errors.New("the built-in default storage class cannot be modified or deleted")
	// ErrInUse is returned when deleting a class that volumes still reference.
	ErrInUse = errors.New("storage class is in use by one or more volumes")
	// ErrNameTaken is returned when registering a class whose name is already taken.
	ErrNameTaken = errors.New("a storage class with this name already exists")
	// ErrDisabled is returned when creating a volume on a class an admin has turned off.
	ErrDisabled = errors.New("storage class is disabled")
	// ErrPathMissing is returned when the class path does not exist on its node.
	ErrPathMissing = errors.New("storage class path does not exist on the node")
	// ErrNotFound is returned when no class matches a name.
	ErrNotFound = errors.New("storage class not found")
	// ErrNameRequired and ErrPathRequired guard the create form.
	ErrNameRequired = errors.New("storage class name is required")
	ErrPathRequired = errors.New("storage class path is required")
)

const (
	// helperTarget is where a class's path is bound inside the helper container. The helper only
	// ever touches paths under it, so a bug cannot reach the rest of the node.
	helperTarget     = "/mnt/class"
	defaultHelperImg = "busybox:1.36"
	helperTimeout    = 2 * time.Minute
	volumeDirMode    = "0755"
)

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// ServerInfo resolves a node's display metadata by id (optional).
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

// Service manages storage classes and the node-side directories they own.
type Service struct {
	repo       *repositories.StorageClassRepository
	clients    NodeDocker
	images     ImageResolver
	serverInfo ServerInfo
}

func NewService(repo *repositories.StorageClassRepository, clients NodeDocker) *Service {
	return &Service{repo: repo, clients: clients}
}

// SetImageResolver wires the deployment-config resolver for the helper image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// SetServerInfo wires the resolver used to annotate classes with their node's name.
func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

func (s *Service) helperImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return defaultHelperImg
}

func (s *Service) annotate(c *models.StorageClass) {
	if c == nil || s.serverInfo == nil {
		return
	}
	if srv, err := s.serverInfo.Get(c.ServerID); err == nil && srv != nil {
		c.ServerName = srv.Label()
	}
}

// EnsureBuiltin seeds the "default" class: volumes created the way Docker creates them, in the
// engine's own data root. Every install has it, every pre-existing volume backfills to it, and it
// is what makes this feature a no-op for an install that registers no classes of its own.
func (s *Service) EnsureBuiltin() error {
	_, err := s.repo.FindByName(models.DefaultStorageClassName)
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.repo.Create(&models.StorageClass{
		Name:          models.DefaultStorageClassName,
		DisplayName:   "Default (Docker managed)",
		Description:   "Volumes are created in the Docker engine's own data directory.",
		IsDefault:     true,
		Enabled:       true,
		Builtin:       true,
		ReclaimPolicy: models.ReclaimDelete,
	})
}

// Input is the editable surface of a storage class. Name, Path and ServerID are read on create only;
// Update refuses a change to Name or Path.
type Input struct {
	Name          string
	DisplayName   string
	Description   string
	ServerID      uint
	ClusterID     uint
	Path          string
	Shared        bool
	IsDefault     bool
	Enabled       bool
	ReclaimPolicy models.ReclaimPolicy
}

// Create registers a class after validating its path and probing the node for it. The probe is the
// point of the design: a class whose disk is not mounted fails here, naming the node, instead of at
// some tenant's first deploy.
func (s *Service) Create(ctx context.Context, in Input) (*models.StorageClass, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if !models.ValidStorageClassName(name) {
		return nil, models.ErrStorageClassName
	}
	exists, err := s.repo.ExistsByName(name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrNameTaken
	}
	clean, err := models.ValidateStorageClassPath(in.Path)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return nil, ErrPathRequired
	}
	policy := in.ReclaimPolicy
	if !models.ValidReclaimPolicy(policy) {
		policy = models.ReclaimDelete
	}
	c := &models.StorageClass{
		Name: name, DisplayName: strings.TrimSpace(in.DisplayName), Description: in.Description,
		ServerID: in.ServerID, ClusterID: in.ClusterID, Path: clean, Shared: in.Shared,
		IsDefault: in.IsDefault, Enabled: in.Enabled, ReclaimPolicy: policy,
	}
	if c.DisplayName == "" {
		c.DisplayName = name
	}
	if err := s.Probe(ctx, c); err != nil {
		return nil, err
	}
	if err := s.repo.Create(c); err != nil {
		return nil, err
	}
	if c.IsDefault {
		if err := s.repo.ClearDefault(c.ServerID, c.ID); err != nil {
			return nil, err
		}
	}
	s.annotate(c)
	return c, nil
}

// Update changes the editable fields. A name or path change is refused, not applied — see
// ErrImmutable.
func (s *Service) Update(id uint, in Input) (*models.StorageClass, error) {
	c, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if n := strings.TrimSpace(in.Name); n != "" && n != c.Name {
		return nil, ErrImmutable
	}
	if p := strings.TrimSpace(in.Path); p != "" {
		clean, verr := models.ValidateStorageClassPath(p)
		if verr != nil {
			return nil, verr
		}
		if clean != c.Path {
			return nil, ErrImmutable
		}
	}
	if c.Builtin && (in.Shared != c.Shared || in.ServerID != c.ServerID) {
		return nil, ErrBuiltin
	}
	if l := strings.TrimSpace(in.DisplayName); l != "" {
		c.DisplayName = l
	}
	c.Description = in.Description
	c.Enabled = in.Enabled
	c.IsDefault = in.IsDefault
	if models.ValidReclaimPolicy(in.ReclaimPolicy) {
		c.ReclaimPolicy = in.ReclaimPolicy
	}
	if !c.Builtin {
		c.Shared = in.Shared
	}
	if err := s.repo.Update(c); err != nil {
		return nil, err
	}
	if c.IsDefault {
		if err := s.repo.ClearDefault(c.ServerID, c.ID); err != nil {
			return nil, err
		}
	}
	s.annotate(c)
	return c, nil
}

// Delete removes a class. The built-in one is permanent, and a class still referenced by volumes is
// refused: disabling it is how an admin stops new volumes landing there without touching old ones.
func (s *Service) Delete(id uint) error {
	c, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if c.Builtin {
		return ErrBuiltin
	}
	n, err := s.repo.CountVolumes(c.Name)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d volume(s) still use %q — disable it instead", ErrInUse, n, c.Name)
	}
	return s.repo.Delete(c.ID)
}

// List returns every registered class, node-annotated.
func (s *Service) List() ([]models.StorageClass, error) {
	out, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	for i := range out {
		s.annotate(&out[i])
	}
	return out, nil
}

// Count reports the size of the class catalog, for the edition cap.
func (s *Service) Count() (int64, error) {
	return s.repo.Count()
}

// ListForNode returns the classes usable on a node: its own, plus every shared one.
func (s *Service) ListForNode(serverID uint) ([]models.StorageClass, error) {
	out, err := s.repo.ListByServer(serverID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		s.annotate(&out[i])
	}
	return out, nil
}

// Get returns one class by id.
func (s *Service) Get(id uint) (*models.StorageClass, error) {
	c, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	s.annotate(c)
	return c, nil
}

// Resolve returns the class a volume should be created on. An empty key takes the node's default
// class and falls back to the built-in one, so an install that registers nothing behaves exactly as
// it did before storage classes existed.
func (s *Service) Resolve(serverID uint, name string) (*models.StorageClass, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if c, err := s.repo.FindDefault(serverID); err == nil {
			return c, nil
		}
		c, err := s.repo.FindByName(models.DefaultStorageClassName)
		if err != nil {
			return nil, ErrNotFound
		}
		return c, nil
	}
	c, err := s.repo.FindByName(name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return nil, err
	}
	if !c.Enabled {
		return nil, fmt.Errorf("%w: %q", ErrDisabled, name)
	}
	// A class belongs to one node unless the operator asserted the path is identical everywhere.
	if c.Managed() && !c.Shared && c.ServerID != serverID {
		return nil, fmt.Errorf("storage class %q is not available on this node", name)
	}
	return c, nil
}

// Probe verifies the class path exists on its node by binding it into a helper container with
// NoCreate set: the daemon refuses a missing source rather than creating an empty directory, which
// is what stops an unmounted disk from silently becoming a directory on the root filesystem. It
// records the filesystem's capacity while it is there.
func (s *Service) Probe(ctx context.Context, c *models.StorageClass) error {
	if !c.Managed() {
		return nil
	}
	total, avail, err := s.measure(ctx, c)
	if err != nil {
		return err
	}
	now := time.Now()
	c.CapacityBytes, c.AvailableBytes, c.MeasuredAt = total, avail, &now
	return nil
}

// EnsureDir creates the per-volume directory under the class path on the node. Docker accepts a
// bind-backed volume whose device is absent and only fails when a container mounts it, so this runs
// before the volume is created.
func (s *Service) EnsureDir(ctx context.Context, c *models.StorageClass, dockerName string) error {
	if !c.Managed() {
		return nil
	}
	sub, err := safeSubdir(dockerName)
	if err != nil {
		return err
	}
	_, err = s.runHelper(ctx, c, []string{"sh", "-c",
		fmt.Sprintf("mkdir -p %[1]s/%[2]s && chmod %[3]s %[1]s/%[2]s", helperTarget, sub, volumeDirMode)})
	return err
}

// RemoveDir deletes the volume's directory under the class path. `docker volume rm` leaves a
// bind-backed volume's data on the disk, so without this a deleted volume would orphan every byte
// it held on the operator's storage. A retain-policy class keeps it for an admin to reclaim.
func (s *Service) RemoveDir(ctx context.Context, c *models.StorageClass, dockerName string) error {
	if !c.Managed() || c.ReclaimPolicy == models.ReclaimRetain {
		return nil
	}
	sub, err := safeSubdir(dockerName)
	if err != nil {
		return err
	}
	_, err = s.runHelper(ctx, c, []string{"rm", "-rf", helperTarget + "/" + sub})
	return err
}

// DiskUsageAll measures every volume directory under a class in one helper run, keyed by Docker
// volume name. `docker system df` sizes a bind-backed volume's mountpoint stub rather than the data
// behind the bind, so a managed class is measured here instead.
func (s *Service) DiskUsageAll(ctx context.Context, c *models.StorageClass) (map[string]int64, error) {
	if !c.Managed() {
		return nil, nil
	}
	out, err := s.runHelper(ctx, c, []string{"sh", "-c",
		fmt.Sprintf("du -sk %s/* 2>/dev/null || true", helperTarget)})
	if err != nil {
		return nil, err
	}
	usage := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, perr := parseKB(fields[0])
		if perr != nil {
			continue
		}
		usage[path.Base(fields[len(fields)-1])] = kb * 1024
	}
	return usage, nil
}

// ListManaged returns the enabled classes that own a path, i.e. the ones with data to measure.
func (s *Service) ListManaged() ([]models.StorageClass, error) {
	all, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	out := make([]models.StorageClass, 0, len(all))
	for i := range all {
		if all[i].Managed() && all[i].Enabled {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// MeasureCapacity refreshes every managed class's filesystem capacity. Best-effort: an unreachable
// node keeps its previous measurement, and the sweep always returns nil so cron does not retry.
func (s *Service) MeasureCapacity(ctx context.Context) error {
	classes, err := s.repo.List()
	if err != nil {
		logger.Warn("storage class sweep: list failed", "error", err)
		return nil
	}
	for i := range classes {
		c := &classes[i]
		if !c.Managed() || !c.Enabled {
			continue
		}
		total, avail, merr := s.measure(ctx, c)
		if merr != nil {
			logger.Warn("storage class sweep: measure failed", "class", c.Name, "server_id", c.ServerID, "error", merr)
			continue
		}
		if err := s.repo.SetCapacity(c.ID, total, avail, time.Now()); err != nil {
			logger.Warn("storage class sweep: record failed", "class", c.Name, "error", err)
		}
	}
	return nil
}

// measure reads the class filesystem's total and available bytes with df inside the helper.
func (s *Service) measure(ctx context.Context, c *models.StorageClass) (total, avail int64, err error) {
	out, err := s.runHelper(ctx, c, []string{"df", "-Pk", helperTarget})
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unreadable df output: %q", out)
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("unreadable df output: %q", out)
	}
	totalKB, err := parseKB(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unreadable df output: %q", out)
	}
	availKB, err := parseKB(fields[3])
	if err != nil {
		return 0, 0, fmt.Errorf("unreadable df output: %q", out)
	}
	return totalKB * 1024, availKB * 1024, nil
}

// runHelper runs one short-lived busybox on the class's node with the class path bound NoCreate.
// The control plane has only the Docker API on a remote node, so every filesystem action a class
// needs — probe, mkdir, reclaim, du — goes through here.
func (s *Service) runHelper(ctx context.Context, c *models.StorageClass, cmd []string) (string, error) {
	dc, err := s.clients.For(c.ServerID)
	if err != nil {
		return "", err
	}
	image := s.helperImage()
	runCtx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	// Only pull when the node does not already have it: PullImage reaches the registry every time,
	// and the capacity sweep runs this per class on a schedule.
	if present, perr := dc.ImageExists(runCtx, image); perr != nil || !present {
		if err := dc.PullImage(runCtx, image, nil); err != nil {
			return "", fmt.Errorf("pull helper image: %w", err)
		}
	}
	exit, out, err := dc.RunOneShot(runCtx, docker.RunSpec{
		Name:       fmt.Sprintf("mb-sc-%s-%d", c.Name, time.Now().UnixNano()),
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{shellJoin(cmd)},
		Binds:      []docker.BindMount{{Source: c.Path, Target: helperTarget, NoCreate: true}},
	})
	if err != nil {
		// A missing source is how the daemon reports "this disk is not mounted here".
		if isMissingSource(err, out) {
			return "", fmt.Errorf("%w: %s on node %d", ErrPathMissing, c.Path, c.ServerID)
		}
		return "", err
	}
	if exit != 0 {
		return out, fmt.Errorf("storage class helper exited %d: %s", exit, strings.TrimSpace(out))
	}
	return out, nil
}

// safeSubdir guards the one input that reaches a filesystem command. DockerName is platform-
// generated, so anything with a separator or a dot in it is a bug, not a user mistake — and a
// reclaim that followed one would delete the wrong tree.
func safeSubdir(dockerName string) (string, error) {
	sub := strings.TrimSpace(dockerName)
	if sub == "" || strings.ContainsAny(sub, "/.\\ \t\n$`\"'") {
		return "", fmt.Errorf("refusing to use volume directory name %q", dockerName)
	}
	return sub, nil
}

func shellJoin(cmd []string) string {
	if len(cmd) == 3 && cmd[0] == "sh" && cmd[1] == "-c" {
		return cmd[2]
	}
	return strings.Join(cmd, " ")
}

func parseKB(s string) (int64, error) {
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

func isMissingSource(err error, out string) bool {
	hay := strings.ToLower(err.Error() + " " + out)
	return strings.Contains(hay, "no such file or directory") ||
		strings.Contains(hay, "bind source path does not exist") ||
		strings.Contains(hay, "mount source path does not exist")
}
