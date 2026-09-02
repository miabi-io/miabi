// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dns

import (
	"github.com/libdns/acmedns"
	"github.com/libdns/azure"
	"github.com/libdns/bunny"
	"github.com/libdns/cloudflare"
	"github.com/libdns/cloudns"
	"github.com/libdns/digitalocean"
	"github.com/libdns/gandi"
	"github.com/libdns/glesys"
	"github.com/libdns/godaddy"
	"github.com/libdns/googleclouddns"
	"github.com/libdns/hetzner"
	"github.com/libdns/huaweicloud"
	"github.com/libdns/infomaniak"
	"github.com/libdns/linode"
	"github.com/libdns/luadns"
	"github.com/libdns/namecheap"
	"github.com/libdns/namesilo"
	"github.com/libdns/netcup"
	"github.com/libdns/ovh"
	"github.com/libdns/powerdns"
	"github.com/libdns/route53"
	"github.com/libdns/scaleway"
	"github.com/libdns/tencentcloud"
	"github.com/libdns/transip"
)

// constructors maps a catalogued type to its libdns client. Credential keys match the
// field keys the type's dnscatalog descriptor declares; required ones are already checked.
var constructors = map[string]func(Credentials) zoneClient{
	"cloudflare":   func(c Credentials) zoneClient { return &cloudflare.Provider{APIToken: c["api_token"]} },
	"digitalocean": func(c Credentials) zoneClient { return &digitalocean.Provider{APIToken: c["api_token"]} },
	"route53": func(c Credentials) zoneClient {
		return &route53.Provider{AccessKeyId: c["access_key_id"], SecretAccessKey: c["secret_access_key"], Region: c["region"]}
	},
	"hetzner": func(c Credentials) zoneClient { return &hetzner.Provider{AuthAPIToken: c["api_token"]} },
	"googleclouddns": func(c Credentials) zoneClient {
		return &googleclouddns.Provider{Project: c["project"], ServiceAccountJSON: c["service_account_json"]}
	},
	"azure": func(c Credentials) zoneClient {
		return &azure.Provider{
			SubscriptionId: c["subscription_id"], ResourceGroupName: c["resource_group"],
			TenantId: c["tenant_id"], ClientId: c["client_id"], ClientSecret: c["client_secret"],
		}
	},
	"linode":  func(c Credentials) zoneClient { return &linode.Provider{APIToken: c["api_token"]} },
	"godaddy": func(c Credentials) zoneClient { return &godaddy.Provider{APIToken: c["api_token"]} },
	"namecheap": func(c Credentials) zoneClient {
		return &namecheap.Provider{
			APIKey: c["api_key"], User: c["username"], ClientIP: c["client_ip"], APIEndpoint: c["api_endpoint"],
		}
	},
	"ovh": func(c Credentials) zoneClient {
		return &ovh.Provider{
			Endpoint: c["endpoint"], ApplicationKey: c["application_key"],
			ApplicationSecret: c["application_secret"], ConsumerKey: c["consumer_key"],
		}
	},
	"gandi": func(c Credentials) zoneClient { return &gandi.Provider{BearerToken: c["api_token"]} },
	"powerdns": func(c Credentials) zoneClient {
		return &powerdns.Provider{ServerURL: c["server_url"], ServerID: c["server_id"], APIToken: c["api_token"]}
	},
	"acmedns": func(c Credentials) zoneClient {
		return &acmedns.Provider{
			ServerURL: c["server_url"], Username: c["username"],
			Password: c["password"], Subdomain: c["subdomain"],
		}
	},
	"scaleway": func(c Credentials) zoneClient {
		return &scaleway.Provider{SecretKey: c["secret_key"], OrganizationID: c["organization_id"]}
	},
	"netcup": func(c Credentials) zoneClient {
		return &netcup.Provider{CustomerNumber: c["customer_number"], APIKey: c["api_key"], APIPassword: c["api_password"]}
	},
	"infomaniak": func(c Credentials) zoneClient { return &infomaniak.Provider{APIToken: c["api_token"]} },
	"transip": func(c Credentials) zoneClient {
		return &transip.Provider{AuthLogin: c["account_name"], PrivateKey: c["private_key"]}
	},
	"glesys": func(c Credentials) zoneClient { return &glesys.Provider{Project: c["project"], APIKey: c["api_key"]} },
	"cloudns": func(c Credentials) zoneClient {
		return &cloudns.Provider{AuthId: c["auth_id"], SubAuthId: c["sub_auth_id"], AuthPassword: c["auth_password"]}
	},
	"tencentcloud": func(c Credentials) zoneClient {
		return &tencentcloud.Provider{SecretId: c["secret_id"], SecretKey: c["secret_key"], Region: c["region"]}
	},
	"huaweicloud": func(c Credentials) zoneClient {
		return &huaweicloud.Provider{
			AccessKeyId: c["access_key_id"], SecretAccessKey: c["secret_access_key"], RegionId: c["region_id"],
		}
	},
	"bunny":    func(c Credentials) zoneClient { return &bunny.Provider{AccessKey: c["access_key"]} },
	"luadns":   func(c Credentials) zoneClient { return &luadns.Provider{Email: c["email"], APIKey: c["api_key"]} },
	"namesilo": func(c Credentials) zoneClient { return &namesilo.Provider{APIToken: c["api_token"]} },
}
