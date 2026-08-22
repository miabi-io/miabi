package config

const (
	// OfficialMarketplaceURL is Miabi's hosted template catalog and the default for
	// MIABI_MARKETPLACE_URL. Exported because it is also the fallback an unlicensed
	// install is held to when it configures a custom marketplace.
	OfficialMarketplaceURL = "https://marketplace.miabi.io"

	marketplaceURL = OfficialMarketplaceURL
	miabiLogDir    = "/var/lib/miabi/logs"
)
