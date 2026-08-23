// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mwcatalog is the single source of truth for the curated Goma middleware types Miabi
// exposes as security policies. Each supported type has one Descriptor driving validation, secret
// handling and the UI form; an uncatalogued type passes through to Goma without schema checks.
package mwcatalog

import (
	"fmt"
	"strings"
)

// Category groups middleware types for the UI.
type Category string

const (
	CategoryAccess        Category = "access"
	CategorySecurity      Category = "security"
	CategoryTraffic       Category = "traffic"
	CategoryTransform     Category = "transform"
	CategoryObservability Category = "observability"
)

// Field types understood by validation and the form renderer.
const (
	FieldString   = "string"
	FieldInt      = "int"
	FieldBool     = "bool"
	FieldStrings  = "string[]"
	FieldInts     = "int[]"
	FieldDuration = "duration" // a Go duration string, e.g. "10m"
	FieldEnum     = "enum"     // one of Options
	FieldUsers    = "users"    // basicAuth users: [{username, password}]
	FieldMap      = "map"      // map<string,string> key/value editor (e.g. setHeaders)
	// FieldPairs is a []string whose entries use Goma's "source: target" mapping syntax, a bare
	// "source" meaning "keep the same name". Stored as strings because that is Goma's wire format;
	// edited as two inputs per row, because nobody should need to know the colon convention.
	FieldPairs = "pairs"
	// FieldObject is a nested object. With Fields it renders a structured sub-form
	// (e.g. cors); without Fields it is a free-form map passed through unchecked.
	FieldObject = "object"
	// FieldList is a repeatable list of objects, each shaped by Fields (e.g. the
	// errorInterceptor errors list, or responseHeaders setCookies).
	FieldList = "list"
)

// Field is one key of a middleware's rule.
type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Secret   bool     `json:"secret,omitempty"` // encrypted at rest, redacted in responses
	Default  any      `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"` // for enum
	Help     string   `json:"help,omitempty"`
	// Fields is the sub-schema for FieldList rows and structured FieldObject groups.
	Fields []Field `json:"fields,omitempty"`

	// --- form presentation ---
	// Placeholder is the example shown in an empty scalar or tag input.
	Placeholder string `json:"placeholder,omitempty"`
	// KeyLabel/ValueLabel name the two columns of a FieldMap or FieldPairs editor. They differ per
	// middleware — a setHeaders row is "Header -> value", a JWT forwardHeaders row is "Header -> claim
	// path" — and getting them wrong is how a form teaches the wrong mental model.
	KeyLabel         string `json:"key_label,omitempty"`
	ValueLabel       string `json:"value_label,omitempty"`
	KeyPlaceholder   string `json:"key_placeholder,omitempty"`
	ValuePlaceholder string `json:"value_placeholder,omitempty"`
	// AddLabel is the "+ Add …" button's noun.
	AddLabel string `json:"add_label,omitempty"`
	// ValueOptional marks a FieldPairs whose target may be left blank, meaning
	// "pass through under the same name".
	ValueOptional bool `json:"value_optional,omitempty"`
}

// Descriptor declares one curated Goma middleware type.
type Descriptor struct {
	Type        string   `json:"type"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Category    Category `json:"category"`
	Fields      []Field  `json:"fields"`
	// MinGatewayVersion is the oldest Goma release that carries this middleware
	// type. Empty means it has been there as long as Miabi has curated it.
	//
	// Advisory, not enforced: Miabi does not read a running gateway's version, so
	// this is shown in the form rather than used to hide the type. A route
	// referencing a type its gateway does not have fails at load with a message
	// naming the type, and this is what tells someone why before they get there.
	MinGatewayVersion string `json:"min_gateway_version,omitempty"`
	// Validate is an optional cross-field rule run after the per-field checks pass, for constraints the
	// field loop can't express. nil for types whose fields fully define them. Not serialized — the
	// client can't run it, and the server enforces it on write anyway.
	Validate func(rule map[string]any) error `json:"-"`
}

// MinGomaOIDCVersion is the first Goma release carrying the `oidc` middleware.
// Kept beside the descriptor that needs it rather than in config, so the catalog
// states its own requirements.
const MinGomaOIDCVersion = "0.14.0"

// secretPath is a secret field and the route to it through nested objects, so a
// key like session.secret is reachable rather than silently stored in clear.
type secretPath struct {
	keys  []string
	field Field
}

func (d Descriptor) secretPaths() []secretPath {
	return collectSecretPaths(d.Fields, nil)
}

func collectSecretPaths(fields []Field, prefix []string) []secretPath {
	var out []secretPath
	for _, f := range fields {
		keys := append(append([]string{}, prefix...), f.Key)
		switch {
		case f.Secret:
			out = append(out, secretPath{keys: keys, field: f})
		case f.Type == FieldObject && len(f.Fields) > 0:
			out = append(out, collectSecretPaths(f.Fields, keys)...)
		}
	}
	return out
}

// registry is the ordered set of supported descriptors. Order is the display
// order in the UI catalog.
var registry = []Descriptor{
	{
		Type:        "basicAuth",
		DisplayName: "Basic authentication",
		Description: "Require a username and password (HTTP Basic) to reach the route.",
		Category:    CategoryAccess,
		Fields: []Field{
			{Key: "users", Label: "Users", Type: FieldUsers, Required: true, Secret: true, Help: "One or more username/password pairs."},
			{Key: "realm", Label: "Realm", Type: FieldString, Default: "Restricted", Help: "Shown by the browser's auth prompt."},
			{Key: "forwardUsername", Label: "Forward username to backend", Type: FieldBool},
		},
	},
	{
		Type:        "jwtAuth",
		DisplayName: "JWT authentication",
		Description: "Require a valid JSON Web Token. Verify with a shared secret (HS*), a public key, or a JWKS endpoint.",
		Category:    CategoryAccess,
		Fields: []Field{
			{Key: "secret", Label: "Signing secret", Type: FieldString, Secret: true, Help: "Shared secret for HMAC algorithms (HS256/384/512)."},
			{Key: "publicKey", Label: "Public key", Type: FieldString, Help: "PEM public key for asymmetric algorithms (RS*/ES*)."},
			{Key: "jwksUrl", Label: "JWKS URL", Type: FieldString, Help: "Endpoint serving the signing keys."},
			{Key: "jwksFile", Label: "JWKS file", Type: FieldString, Help: "Path to a local JWKS file."},
			{Key: "algorithms", Label: "Algorithms", Type: FieldStrings, Placeholder: "RS256", AddLabel: "algorithm",
				Help: "Accepted signing algorithms. Empty uses a safe set for the key type you configured."},
			{Key: "issuer", Label: "Issuer", Type: FieldString, Help: "Required iss claim."},
			{Key: "audience", Label: "Audience", Type: FieldString, Help: "Required aud claim."},
			{Key: "claimsExpression", Label: "Claims expression", Type: FieldString,
				Placeholder: "Equals('email_verified', true)",
				Help:        "Expression the token claims must satisfy, e.g. OneOf('role', 'admin', 'ops')."},
			{Key: "forwardAuthorization", Label: "Forward Authorization header", Type: FieldBool,
				Help: "Pass the original Authorization header on to the app instead of stripping it."},
			{Key: "forwardHeaders", Label: "Forward claims as headers", Type: FieldMap,
				KeyLabel: "Header", ValueLabel: "Claim path",
				KeyPlaceholder: "X-User-ID", ValuePlaceholder: "sub", AddLabel: "claim header",
				Help: "Deprecated: use \"Send identity to the app\" below. Each row sends one claim from the verified " +
					"token to your app as a request header."},
			{Key: "forward", Label: "Send identity to the app", Type: FieldObject, Fields: []Field{
				{Key: "headers", Label: "Claims as headers", Type: FieldMap,
					KeyLabel: "Header", ValueLabel: "Claim path",
					KeyPlaceholder: "X-User-ID", ValuePlaceholder: "sub", AddLabel: "claim header",
					Help: "Dot notation reaches nested claims (user.role); booleans arrive as \"true\"/\"false\", and a " +
						"claim the token does not carry is skipped rather than sent empty."},
				{Key: "query", Label: "Claims as query parameters", Type: FieldMap,
					KeyLabel: "Parameter", ValueLabel: "Claim path", KeyPlaceholder: "uid", ValuePlaceholder: "sub", AddLabel: "parameter"},
				{Key: "cookies", Label: "Claims as cookies", Type: FieldMap,
					KeyLabel: "Cookie", ValueLabel: "Claim path", KeyPlaceholder: "app_user", ValuePlaceholder: "email", AddLabel: "cookie"},
				{Key: "stripInbound", Label: "Strip client-supplied copies", Type: FieldBool, Default: true,
					Help: "Leave on. Off lets a caller send these headers itself and choose who your app thinks it is."},
				{Key: "arraySeparator", Label: "Array separator", Type: FieldString, Placeholder: ","},
				{Key: "encoding", Label: "Encoding", Type: FieldEnum, Options: []string{"auto", "raw"}},
				{Key: "maxValueBytes", Label: "Maximum value size", Type: FieldInt, Placeholder: "4096"},
			}},
		},
	},
	{
		Type:              "oidc",
		DisplayName:       "OpenID Connect (SSO)",
		Description:       "Sign users in at the gateway against an identity provider, and pass who they are to the app.",
		Category:          CategoryAccess,
		MinGatewayVersion: MinGomaOIDCVersion,
		Fields: []Field{
			{Key: "issuer", Label: "Issuer URL", Type: FieldString, Placeholder: "https://id.example.com/application/o/app/",
				Help: "The provider's issuer. Everything else is discovered from it, so the endpoints below are only for a provider without a discovery document."},
			{Key: "clientId", Label: "Client ID", Type: FieldString, Required: true},
			{Key: "clientSecret", Label: "Client secret", Type: FieldString, Required: true, Secret: true},
			{Key: "provider", Label: "Provider", Type: FieldEnum,
				Options: []string{"custom", "google", "github", "gitlab", "amazon", "facebook"},
				Help:    "A well-known provider fills in its own endpoints."},
			{Key: "scopes", Label: "Scopes", Type: FieldStrings, Placeholder: "openid", AddLabel: "scope",
				Help: "Requested scopes. Most providers need at least openid, email and profile."},
			{Key: "audience", Label: "Audience", Type: FieldString,
				Help: "Enforced as the aud claim on JWT access tokens. The ID token is always checked against the client ID."},

			{Key: "endpoint", Label: "Endpoints", Type: FieldObject,
				Help: "Only needed without an issuer, or to override what discovery returns.",
				Fields: []Field{
					{Key: "authUrl", Label: "Authorization URL", Type: FieldString},
					{Key: "tokenUrl", Label: "Token URL", Type: FieldString},
					{Key: "jwksUrl", Label: "JWKS URL", Type: FieldString, Help: "Verifies JWT access tokens and ID tokens."},
					{Key: "userInfoUrl", Label: "User info URL", Type: FieldString, Help: "Verifies opaque tokens, and supplies claims."},
				}},

			{Key: "callbackPath", Label: "Callback path", Type: FieldString, Placeholder: "/oauth2/callback",
				Help: "Where the provider returns the user. Register this exact path with the provider. Defaults to the route path plus /oauth2/callback."},
			{Key: "logoutPath", Label: "Logout path", Type: FieldString, Placeholder: "/oauth2/logout",
				Help: "Ends the session when requested. Not served unless set."},
			{Key: "postLoginRedirect", Label: "After sign-in", Type: FieldString, Placeholder: "/dashboard",
				Help: "Empty returns the user to the page they asked for."},
			{Key: "postLogoutRedirect", Label: "After sign-out", Type: FieldString, Placeholder: "/"},
			{Key: "pkce", Label: "Use PKCE", Type: FieldBool, Default: true,
				Help: "Proof key on the code exchange. Leave on unless the provider rejects it."},

			{Key: "session", Label: "Session", Type: FieldObject, Fields: []Field{
				{Key: "store", Label: "Store", Type: FieldEnum, Options: []string{"cookie", "memory", "redis"},
					Help: "Cookie needs no shared state. Redis shares sessions across gateway replicas and needs Redis configured on the gateway."},
				{Key: "secret", Label: "Session secret", Type: FieldString, Secret: true,
					Help: "Keys the sealing of session data. Defaults to the client secret; changing it signs everyone out."},
				{Key: "ttl", Label: "Maximum age", Type: FieldDuration, Placeholder: "12h",
					Help: "How long a session lives regardless of activity."},
				{Key: "idleTimeout", Label: "Idle timeout", Type: FieldDuration, Placeholder: "1h"},
				{Key: "cookie", Label: "Cookie", Type: FieldObject, Fields: []Field{
					{Key: "name", Label: "Name", Type: FieldString, Placeholder: "goma_session"},
					{Key: "path", Label: "Path", Type: FieldString, Placeholder: "/",
						Help: "Defaults to the route path, so two routes on one host do not share a session."},
					{Key: "domain", Label: "Domain", Type: FieldString},
					{Key: "sameSite", Label: "SameSite", Type: FieldEnum, Options: []string{"lax", "strict", "none"},
						Help: "Strict breaks the provider's callback redirect."},
					{Key: "secure", Label: "Secure", Type: FieldBool, Help: "Defaults to whether the request arrived over TLS."},
				}},
			}},

			{Key: "claimsExpression", Label: "Authorization rule", Type: FieldString,
				Placeholder: "Contains('groups', 'engineering')",
				Help:        "Who is allowed in once signed in. Users who do not match get 403."},
			{Key: "claimsSource", Label: "Claim sources", Type: FieldStrings, Placeholder: "id_token", AddLabel: "source",
				Help: "Where claims are read from, in increasing precedence: access_token, userinfo, id_token."},

			{Key: "forward", Label: "Send identity to the app", Type: FieldObject, Fields: []Field{
				{Key: "headers", Label: "Claims as headers", Type: FieldMap,
					KeyLabel: "Header", ValueLabel: "Claim path",
					KeyPlaceholder: "X-Auth-Email", ValuePlaceholder: "email", AddLabel: "claim header",
					Help: "Dot notation reaches nested claims; a template like \"{{ .given_name }} {{ .family_name }}\" joins several."},
				{Key: "query", Label: "Claims as query parameters", Type: FieldMap,
					KeyLabel: "Parameter", ValueLabel: "Claim path", KeyPlaceholder: "uid", ValuePlaceholder: "sub", AddLabel: "parameter"},
				{Key: "cookies", Label: "Claims as cookies", Type: FieldMap,
					KeyLabel: "Cookie", ValueLabel: "Claim path", KeyPlaceholder: "app_user", ValuePlaceholder: "email", AddLabel: "cookie",
					Help: "Added to the request sent upstream, never to the browser's response."},
				{Key: "stripInbound", Label: "Strip client-supplied copies", Type: FieldBool, Default: true,
					Help: "Leave on. Off lets a caller send these headers itself and choose who your app thinks it is."},
				{Key: "arraySeparator", Label: "Array separator", Type: FieldString, Placeholder: ","},
				{Key: "encoding", Label: "Encoding", Type: FieldEnum, Options: []string{"auto", "raw"},
					Help: "Auto base64-encodes non-ASCII values, which headers cannot carry, and flags them."},
				{Key: "maxValueBytes", Label: "Maximum value size", Type: FieldInt, Placeholder: "4096"},
				{Key: "accessTokenHeader", Label: "Forward the access token", Type: FieldString, Placeholder: "Authorization",
					Help: "Lets the app verify the token itself instead of trusting a header."},
				{Key: "idTokenHeader", Label: "Forward the ID token", Type: FieldString, Placeholder: "X-Auth-Id-Token"},
			}},
		},
		Validate: validateOIDC,
	},
	{
		Type:        "forwardAuth",
		DisplayName: "Forward authentication",
		Description: "Delegate authentication to an external service, like Authelia or oauth2-proxy.",
		Category:    CategoryAccess,
		Fields: []Field{
			{Key: "authUrl", Label: "Auth URL", Type: FieldString, Required: true, Help: "Service that authorizes each request (2xx = allow)."},
			{Key: "authSignIn", Label: "Sign-in URL", Type: FieldString, Help: "Where to redirect unauthenticated users."},
			{Key: "forwardHostHeaders", Label: "Forward host headers", Type: FieldBool},
			{Key: "insecureSkipVerify", Label: "Skip TLS verification", Type: FieldBool, Help: "Don't verify the auth service's TLS certificate."},
			{Key: "authRequestHeaders", Label: "Send to the auth service", Type: FieldStrings,
				Placeholder: "Authorization", AddLabel: "header",
				Help: "Request headers copied onto the call to the auth service. Empty sends the default set."},
			{Key: "authResponseHeaders", Label: "Auth response → request headers", Type: FieldPairs,
				KeyLabel: "Auth response header", ValueLabel: "Request header", ValueOptional: true,
				KeyPlaceholder: "x-user-id", ValuePlaceholder: "X-Auth-User-ID", AddLabel: "header mapping",
				Help: "Headers the auth service returns, forwarded to your app. Leave the second column blank to keep the same name."},
			{Key: "authResponseHeadersAsParams", Label: "Auth response → query parameters", Type: FieldPairs,
				KeyLabel: "Auth response header", ValueLabel: "Query parameter", ValueOptional: true,
				KeyPlaceholder: "x-user-id", ValuePlaceholder: "userId", AddLabel: "parameter mapping",
				Help: "Same idea, but the value arrives as a query parameter instead of a header."},
			{Key: "addAuthCookiesToResponse", Label: "Cookies to return to the client", Type: FieldStrings,
				Placeholder: "session_id", AddLabel: "cookie",
				Help: "Cookies set by the auth service that should reach the browser. Empty returns all of them."},
		},
	},
	{
		Type:        "ldapAuth",
		DisplayName: "LDAP authentication",
		Description: "Authenticate users against an LDAP / Active Directory directory.",
		Category:    CategoryAccess,
		Fields: []Field{
			{Key: "url", Label: "Server URL", Type: FieldString, Required: true, Help: "e.g. ldap://ldap.example.com:389."},
			{Key: "baseDN", Label: "Base DN", Type: FieldString, Required: true, Help: "e.g. ou=users,dc=example,dc=com."},
			{Key: "bindDN", Label: "Bind DN", Type: FieldString, Required: true, Help: "DN used to bind for user lookups."},
			{Key: "bindPass", Label: "Bind password", Type: FieldString, Required: true, Secret: true},
			{Key: "userFilter", Label: "User filter", Type: FieldString, Required: true, Help: "e.g. (uid=%s)."},
			{Key: "realm", Label: "Realm", Type: FieldString, Help: "Shown by the browser's auth prompt."},
			{Key: "forwardUsername", Label: "Forward username to backend", Type: FieldBool},
			{Key: "startTLS", Label: "StartTLS", Type: FieldBool, Help: "Upgrade the connection to TLS."},
			{Key: "insecureSkipVerify", Label: "Skip TLS verification", Type: FieldBool},
			{Key: "connPool", Label: "Connection pool", Type: FieldObject, Help: "Reuse LDAP connections for bind lookups.", Fields: []Field{
				{Key: "size", Label: "Size", Type: FieldInt, Help: "Max pooled connections."},
				{Key: "burst", Label: "Burst", Type: FieldInt, Help: "Extra connections allowed in a spike."},
				{Key: "ttl", Label: "TTL", Type: FieldDuration, Help: "How long a pooled connection lives, e.g. 30s."},
			}},
		},
	},
	{
		Type:        "access",
		DisplayName: "Block access",
		Description: "Deny requests to the matched paths with a fixed status code.",
		Category:    CategorySecurity,
		Fields: []Field{
			{Key: "statusCode", Label: "Status code", Type: FieldInt, Default: 403, Help: "HTTP status returned for blocked requests (default 403)."},
		},
	},
	{
		Type:        "accessPolicy",
		DisplayName: "IP access policy",
		Description: "Allow or deny requests by client IP / CIDR range.",
		Category:    CategorySecurity,
		Fields: []Field{
			{Key: "action", Label: "Action", Type: FieldEnum, Required: true, Options: []string{"ALLOW", "DENY"}, Default: "ALLOW"},
			{Key: "sourceRanges", Label: "Source ranges", Type: FieldStrings, Required: true,
				Placeholder: "10.0.0.0/8", AddLabel: "range",
				Help: "Client IPs or CIDR ranges this policy applies to. Press Enter or comma to add each one."},
		},
	},
	{
		Type:        "bodyLimit",
		DisplayName: "Request body limit",
		Description: "Reject requests whose body exceeds a size limit.",
		Category:    CategorySecurity,
		Fields: []Field{
			{Key: "limit", Label: "Limit", Type: FieldString, Required: true, Help: "Max body size, e.g. 10MB, 512KB."},
		},
	},
	{
		Type:        "rateLimit",
		DisplayName: "Rate limit",
		Description: "Throttle requests per client over a time unit.",
		Category:    CategoryTraffic,
		Fields: []Field{
			{Key: "unit", Label: "Per", Type: FieldEnum, Required: true, Options: []string{"second", "minute", "hour"}, Default: "minute"},
			{Key: "requestsPerUnit", Label: "Requests per unit", Type: FieldInt, Required: true, Default: 100},
			{Key: "burst", Label: "Burst", Type: FieldInt, Help: "Extra requests allowed in a short spike."},
			{Key: "banAfter", Label: "Ban after", Type: FieldInt, Help: "Ban a client after this many rejected requests."},
			{Key: "banDuration", Label: "Ban duration", Type: FieldDuration, Default: "10m", Help: "How long a banned client stays blocked, e.g. 10m."},
			{Key: "keyStrategy", Label: "Key strategy", Type: FieldObject, Help: "How clients are identified for throttling.", Fields: []Field{
				{Key: "source", Label: "Source", Type: FieldEnum, Options: []string{"ip", "header", "cookie"}, Help: "What identifies a client."},
				{Key: "name", Label: "Name", Type: FieldString, Help: "Header or cookie name (when source is header/cookie)."},
			}},
		},
	},
	{
		Type:        "redirectScheme",
		DisplayName: "Force scheme (HTTPS)",
		Description: "Redirect requests to a different scheme — typically http→https.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "scheme", Label: "Scheme", Type: FieldEnum, Required: true, Options: []string{"https", "http"}, Default: "https"},
			{Key: "port", Label: "Port", Type: FieldInt, Help: "Optional target port (e.g. 443)."},
			{Key: "permanent", Label: "Permanent (301)", Type: FieldBool, Help: "Use 301 instead of 302."},
		},
	},
	{
		Type:        "redirect",
		DisplayName: "Redirect",
		Description: "Redirect every matched request to a fixed URL.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "url", Label: "Destination URL", Type: FieldString, Required: true, Help: "Full target URL including scheme, e.g. https://example.com."},
			{Key: "permanent", Label: "Permanent (301)", Type: FieldBool, Help: "Use 301 instead of 302."},
		},
	},
	{
		Type:        "redirectRegex",
		DisplayName: "Redirect (regex)",
		Description: "Redirect using a regular-expression match on the request path.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "pattern", Label: "Pattern", Type: FieldString, Required: true, Help: "Regex matched against the request path, e.g. ^/old/(.*)."},
			{Key: "replacement", Label: "Replacement", Type: FieldString, Required: true, Help: "Target, with capture references, e.g. https://example.com/new/$1."},
			{Key: "permanent", Label: "Permanent (301)", Type: FieldBool, Help: "Use 301 instead of 302."},
		},
	},
	{
		Type:        "rewriteRegex",
		DisplayName: "Rewrite path (regex)",
		Description: "Rewrite the request path with a regular expression before it reaches the app.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "pattern", Label: "Pattern", Type: FieldString, Required: true, Placeholder: "^/old/(.*)",
				Help: "Regular expression matched against the request path."},
			{Key: "replacement", Label: "Replacement", Type: FieldString, Required: true, Placeholder: "/new/$1",
				Help: "Replacement path. Use $1, $2… for the pattern's capture groups."},
		},
	},
	{
		Type:        "httpCache",
		DisplayName: "HTTP cache",
		Description: "Cache responses at the gateway so repeat requests never reach the app.",
		Category:    CategoryTraffic,
		Fields: []Field{
			{Key: "maxTtl", Label: "Max TTL (s)", Type: FieldInt, Help: "Longest a response is kept, in seconds."},
			{Key: "maxStale", Label: "Max stale (s)", Type: FieldInt,
				Help: "How long a stale entry may still be served while it refreshes."},
			{Key: "memoryLimit", Label: "Memory limit", Type: FieldString, Placeholder: "100MB",
				Help: "Cap on the in-memory cache, e.g. 100MB."},
			{Key: "cacheableStatusCodes", Label: "Cacheable statuses", Type: FieldInts, Placeholder: "200", AddLabel: "status",
				Help: "Response codes worth caching. Empty uses Goma's defaults."},
			{Key: "excludedResponseCodes", Label: "Never cache statuses", Type: FieldStrings, Placeholder: "5xx", AddLabel: "status",
				Help: "Codes or ranges to keep out of the cache, e.g. 5xx."},
			{Key: "includeQueryInKey", Label: "Include query string in the cache key", Type: FieldBool,
				Help: "Off means /page?a=1 and /page?a=2 share one cached response."},
			{Key: "queryParamsToCache", Label: "Query parameters in the key", Type: FieldStrings, Placeholder: "page", AddLabel: "parameter",
				Help: "Restrict the key to these parameters, so tracking parameters don't fragment the cache."},
			{Key: "disableCacheStatusHeader", Label: "Hide the X-Cache-Status header", Type: FieldBool,
				Help: "By default Goma reports HIT/MISS on each response."},
			{Key: "cachePrivateResponses", Label: "Cache authenticated responses", Type: FieldBool,
				Help: "Off by default: a request carrying an Authorization header or a cookie skips the cache entirely, " +
					"since one shared entry would be served to every caller. On, each caller gets their own entry."},
		},
	},
	{
		Type:        "addPrefix",
		DisplayName: "Add path prefix",
		Description: "Prepend a path prefix before forwarding the request to the app.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "prefix", Label: "Prefix", Type: FieldString, Required: true, Help: "Must start with /, e.g. /api."},
		},
		Validate: func(rule map[string]any) error {
			// Goma uses the prefix verbatim (no leading-slash enforcement), so a
			// prefix without / silently produces a broken upstream path. Reject it.
			if p, _ := rule["prefix"].(string); !strings.HasPrefix(p, "/") {
				return fmt.Errorf("%w: %q must start with /", ErrInvalidRule, "prefix")
			}
			return nil
		},
	},
	{
		Type:        "userAgentBlock",
		DisplayName: "Block user agents",
		Description: "Reject requests whose User-Agent matches any of the listed patterns.",
		Category:    CategorySecurity,
		Fields: []Field{
			{Key: "userAgents", Label: "User agents", Type: FieldStrings, Required: true, Placeholder: "Googlebot", AddLabel: "pattern",
				Help: "Substrings matched case-insensitively against the User-Agent header."},
		},
	},
	{
		Type:        "requestHeaders",
		DisplayName: "Request headers",
		Description: "Add, override or remove headers before the request reaches the app.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "setHeaders", Label: "Set headers", Type: FieldMap,
				KeyLabel: "Header", ValueLabel: "Value", KeyPlaceholder: "X-Forwarded-User", ValuePlaceholder: "value", AddLabel: "header",
				Help: "Set on the request before it reaches your app. An empty value removes a client-supplied header."},
			{Key: "removeHeaders", Label: "Remove headers", Type: FieldStrings, Placeholder: "X-Powered-By", AddLabel: "header",
				Help: "Dropped before forwarding, applied before Set headers."},
		},
		Validate: requireAnyOf("setHeaders", "removeHeaders"),
	},
	{
		Type:        "responseHeaders",
		DisplayName: "Response headers",
		Description: "Add, override or remove headers on the response, and set CORS or cookies.",
		Category:    CategoryTransform,
		Fields: []Field{
			{Key: "setHeaders", Label: "Set headers", Type: FieldMap,
				KeyLabel: "Header", ValueLabel: "Value", KeyPlaceholder: "X-Frame-Options", ValuePlaceholder: "DENY", AddLabel: "header",
				Help: "Set on the response. An empty value removes a header your app sent."},
			{Key: "cacheControl", Label: "Cache-Control", Type: FieldString, Help: "Value for the Cache-Control response header, e.g. no-store."},
			{Key: "cacheStatuses", Label: "Cacheable statuses", Type: FieldInts, Placeholder: "200", AddLabel: "status"},
			{Key: "cors", Label: "CORS", Type: FieldObject, Fields: []Field{
				{Key: "enabled", Label: "Enabled", Type: FieldBool},
				{Key: "origins", Label: "Allowed origins", Type: FieldStrings, Placeholder: "https://example.com", AddLabel: "origin",
					Help: "Cannot be * when credentials are allowed."},
				{Key: "allowMethods", Label: "Allowed methods", Type: FieldStrings, Placeholder: "GET", AddLabel: "method"},
				{Key: "allowedHeaders", Label: "Allowed headers", Type: FieldStrings, Placeholder: "Content-Type", AddLabel: "header"},
				{Key: "exposeHeaders", Label: "Exposed headers", Type: FieldStrings, Placeholder: "X-Request-Id", AddLabel: "header"},
				{Key: "allowCredentials", Label: "Allow credentials", Type: FieldBool},
				{Key: "maxAge", Label: "Max age (s)", Type: FieldInt, Help: "Preflight cache lifetime in seconds."},
			}},
			{Key: "setCookies", Label: "Set cookies", Type: FieldList, Fields: []Field{
				{Key: "name", Label: "Name", Type: FieldString, Required: true},
				{Key: "value", Label: "Value", Type: FieldString},
				{Key: "attributes", Label: "Attributes", Type: FieldObject, Fields: []Field{
					{Key: "path", Label: "Path", Type: FieldString},
					{Key: "domain", Label: "Domain", Type: FieldString},
					{Key: "maxAge", Label: "Max age (s)", Type: FieldInt, Help: "0 = session, -1 = delete, >0 = persistent."},
					{Key: "secure", Label: "Secure", Type: FieldBool},
					{Key: "httpOnly", Label: "HttpOnly", Type: FieldBool},
					{Key: "sameSite", Label: "SameSite", Type: FieldEnum, Options: []string{"Strict", "Lax", "None"}},
				}},
			}},
		},
	},
	{
		Type:        "errorInterceptor",
		DisplayName: "Error interceptor",
		Description: "Replace upstream error responses with a custom body or template.",
		Category:    CategoryObservability,
		Fields: []Field{
			{Key: "enabled", Label: "Enabled", Type: FieldBool, Required: true, Default: true},
			{Key: "contentType", Label: "Content type", Type: FieldString, Default: "application/json", Help: "Content-Type of the custom bodies, e.g. application/json."},
			{Key: "errors", Label: "Errors", Type: FieldList, Required: true, Help: "Status codes to intercept. Set a body or file, or neither to pass through.", Fields: []Field{
				{Key: "statusCode", Label: "Status code", Type: FieldInt, Required: true, Help: "HTTP status to intercept, e.g. 404."},
				{Key: "body", Label: "Body", Type: FieldString, Help: "Custom response body (JSON or text)."},
				{Key: "file", Label: "File path", Type: FieldString, Help: "Path to a template file served instead of a body."},
			}},
		},
	},
	{
		Type:        "geoBlock",
		DisplayName: "Country access policy (GeoIP)",
		Description: "Allow or deny requests by client country (GeoIP), with optional country-header enrichment for the backend.",
		Category:    CategorySecurity,
		Fields: []Field{
			{Key: "action", Label: "Action", Type: FieldEnum, Required: true, Options: []string{"ALLOW", "DENY"}, Default: "ALLOW", Help: "ALLOW = allowlist (only these countries pass); DENY = blocklist."},
			{Key: "countries", Label: "Countries", Type: FieldStrings, Required: true, Help: "ISO 3166-1 alpha-2 codes, e.g. US, FR, DE."},
			{Key: "allowUnknown", Label: "Allow unknown country", Type: FieldBool, Default: true, Help: "When the country can't be resolved (no GeoIP database, private IP), allow the request. Off = block (fail-closed)."},
			{Key: "addCountryHeader", Label: "Add country header", Type: FieldString, Help: "Inject the resolved country to the backend under this header, e.g. X-Country-Code."},
			{Key: "statusCode", Label: "Status code", Type: FieldInt, Default: 403, Help: "HTTP status returned for a blocked request."},
			{Key: "message", Label: "Message", Type: FieldString, Help: "Response body for a blocked request."},
		},
		// Requires a GeoIP database on the gateway (GOMA_GEOIP_DB). Miabi does not
		// install one — the operator drops country.mmdb beside goma.yml. Without it
		// no country resolves, so every rule here falls through to allowUnknown.
		Validate: func(rule map[string]any) error {
			raw, _ := rule["countries"].([]any)
			for _, c := range raw {
				s, _ := c.(string)
				if len(strings.TrimSpace(s)) != 2 {
					return fmt.Errorf("%w: country %q must be an ISO 3166-1 alpha-2 code (e.g. US)", ErrInvalidRule, s)
				}
			}
			return nil
		},
	},
}

var byType = func() map[string]Descriptor {
	m := make(map[string]Descriptor, len(registry))
	for _, d := range registry {
		m[d.Type] = d
	}
	return m
}()

// Get returns the descriptor for a Goma middleware type, if catalogued.
func Get(t string) (Descriptor, bool) {
	d, ok := byType[t]
	return d, ok
}

// All returns every catalogued descriptor in display order.
func All() []Descriptor {
	out := make([]Descriptor, len(registry))
	copy(out, registry)
	return out
}
