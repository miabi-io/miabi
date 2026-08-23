package config

const (
	// OfficialMarketplaceURL is Miabi's hosted template catalog and the default for
	// MIABI_MARKETPLACE_URL. Exported because it is also the fallback an unlicensed
	// install is held to when it configures a custom marketplace.
	OfficialMarketplaceURL = "https://marketplace.miabi.io"
	DefaultGomaImage       = "jkaninda/goma-gateway:" + DefaultGomaVersion

	// DefaultGomaVersion is the gateway version this build provisions and is
	// tested against. Keep it in step with GOMA_VERSION in the release workflow.
	DefaultGomaVersion = "0.14.0"

	marketplaceURL = OfficialMarketplaceURL
	miabiLogDir    = "/var/lib/miabi/logs"
)
