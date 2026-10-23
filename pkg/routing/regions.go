package routing

// Stacks whose region names the table carries. A stack only ever sends a
// client a token from this table: a name the client binary does not know can
// hang it (Outbound spun on a made-up Photon token).
const (
	StackPhoton  = "photon"  // Photon region tokens (GetRegions, param 210 "sa/*")
	StackAmongUs = "amongus" // Among Us matchmaker region ("na", "eu", "as")
	StackPlayFab = "playfab" // PlayFab Party / Azure region names
	StackGCP     = "gcp"     // GCP region names (UGS QoS, Splatoon 3 latency servers)
	StackNPLN    = "npln"    // NPLN relays: no region names, country only
)

// DefaultService is the node role a stack's rooms need when the request does
// not name one.
var DefaultService = map[string]string{
	StackPhoton:  "photon",
	StackAmongUs: "impostor",
	StackPlayFab: "playfab_party",
	StackGCP:     "stun",
	StackNPLN:    "turn",
}

// HomeRegion is the region home itself serves. Home answers for it: a node
// there would only add capacity, which is not wanted yet.
const HomeRegion = "eu-west"

// Builtin is the region registry: codes, the countries each serves, the
// fallback chain (home is implied at the end) and every stack's names.
//
// Order matters where one token names several regions (Among Us "na"/"as",
// Photon "eu"): the first region listing a token is its primary, used when the
// account country does not pick one of the others.
//
// Vendor names are the ones the SDKs ship (federation-worldwide-plan.md §1.3).
// Mainland China and Russia take no nodes; their players are routed to the
// neighbours through CountryRoutes.
func Builtin() []Region {
	return []Region{
		{Code: "eu-west", Name: "Europe (home)",
			Countries: []string{"GB", "IE", "FR", "DE", "NL", "BE", "LU", "PT", "ES", "AD", "MC", "IT", "SM", "VA", "MT", "CH", "LI", "AT",
				"DK", "NO", "SE", "IS", "FO", "PL", "CZ", "SK", "HU", "SI", "HR", "BA", "RS", "ME", "XK", "MK", "AL", "GR", "CY",
				"BG", "RO", "MD", "UA", "GI", "MA", "DZ", "TN"},
			Aliases: map[string][]string{
				StackPhoton:  {"eu"},
				StackPlayFab: {"UkSouth", "WestEurope", "NorthEurope", "FranceCentral"},
				StackGCP:     {"europe-west2", "europe-central2", "europe-west1", "europe-west3", "europe-west4"},
				StackAmongUs: {"eu"},
			}},
		{Code: "us-east", Name: "North America East",
			Countries: []string{"US", "CA", "PR", "VI", "BS", "CU", "JM", "HT", "DO", "TT", "BB", "AG", "DM", "GD", "KN", "LC", "VC",
				"BM", "KY", "TC", "AW", "CW", "BZ", "GT", "SV", "HN", "NI", "CR", "PA", "CO", "VE", "EC", "GY", "SR"},
			Aliases: map[string][]string{
				StackPhoton:  {"us", "cae", "ussc"},
				StackPlayFab: {"EastUs", "EastUs2", "CentralUs", "SouthCentralUs", "NorthCentralUs"},
				StackGCP:     {"northamerica-northeast1", "us-east1", "us-east4", "us-central1"},
				StackAmongUs: {"na"},
			}},
		{Code: "us-west", Name: "North America West",
			Countries: []string{"MX"},
			Fallback:  []string{"us-east"},
			Aliases: map[string][]string{
				StackPhoton:  {"usw"},
				StackPlayFab: {"WestUs", "WestUs2", "WestCentralUs"},
				StackGCP:     {"us-west2", "us-west1"},
				StackAmongUs: {"na"},
			}},
		{Code: "sa-east", Name: "South America",
			Countries: []string{"BR", "AR", "CL", "UY", "PY", "PE", "BO"},
			Fallback:  []string{"us-east"},
			Aliases: map[string][]string{
				StackPhoton:  {"sa"},
				StackPlayFab: {"BrazilSouth"},
				StackGCP:     {"southamerica-east1", "southamerica-west1"},
				StackAmongUs: {"na"},
			}},
		{Code: "ap-southeast", Name: "Southeast Asia",
			Countries: []string{"SG", "MY", "TH", "VN", "ID", "PH", "KH", "LA", "MM", "BN", "TL"},
			Aliases: map[string][]string{
				StackPhoton:  {"asia"},
				StackPlayFab: {"SoutheastAsia"},
				StackGCP:     {"asia-southeast1", "asia-southeast2"},
				StackAmongUs: {"as"},
			}},
		{Code: "ap-northeast", Name: "Japan and Korea",
			Countries: []string{"JP", "KR"},
			Fallback:  []string{"ap-southeast"},
			Aliases: map[string][]string{
				StackPhoton:  {"jp", "kr"},
				StackPlayFab: {"JapanEast", "JapanWest", "KoreaCentral"},
				StackGCP:     {"asia-northeast1", "asia-northeast2", "asia-northeast3"},
				StackAmongUs: {"as"},
			}},
		{Code: "ap-east", Name: "Hong Kong and Taiwan",
			Countries: []string{"HK", "TW", "MO", "MN"},
			Fallback:  []string{"ap-northeast", "ap-southeast"},
			Aliases: map[string][]string{
				StackPhoton:  {"hk"},
				StackPlayFab: {"EastAsia"},
				StackGCP:     {"asia-east2", "asia-east1"},
				StackAmongUs: {"as"},
			}},
		{Code: "oc", Name: "Oceania",
			Countries: []string{"AU", "NZ", "FJ", "PG", "NC", "PF", "WS", "TO", "VU", "SB", "GU"},
			Fallback:  []string{"ap-southeast"},
			Aliases: map[string][]string{
				StackPhoton:  {"au"},
				StackPlayFab: {"AustraliaEast", "AustraliaSoutheast"},
				StackGCP:     {"australia-southeast1", "australia-southeast2"},
				StackAmongUs: {"as"},
			}},
		{Code: "ap-south", Name: "South Asia",
			Countries: []string{"IN", "PK", "BD", "LK", "NP", "BT", "MV"},
			Fallback:  []string{"ap-southeast"},
			Aliases: map[string][]string{
				StackPhoton:  {"in"},
				StackPlayFab: {"CentralIndia"},
				StackGCP:     {"asia-south1", "asia-south2"},
				StackAmongUs: {"as"},
			}},
		{Code: "me", Name: "Middle East",
			Countries: []string{"AE", "SA", "QA", "KW", "BH", "OM", "IL", "PS", "JO", "LB", "IQ", "TR", "EG", "GE", "AM", "AZ"},
			Aliases: map[string][]string{
				StackPhoton:  {"uae", "tr"},
				StackPlayFab: {"UaeNorth"},
				StackGCP:     {"me-central1", "me-west1"},
				StackAmongUs: {"eu"},
			}},
		{Code: "af-south", Name: "Southern Africa",
			Countries: []string{"ZA", "NA", "BW", "ZW", "MZ", "LS", "SZ", "ZM", "MW"},
			Aliases: map[string][]string{
				StackPhoton:  {"za"},
				StackPlayFab: {"SouthAfricaNorth"},
				StackGCP:     {"africa-south1"},
				StackAmongUs: {"eu"},
			}},
		// Finland and the Baltics: Europe's north-east, and where Russian
		// players go (no nodes in Russia). Photon has no token of its own
		// here; "eu" reaches it for players whose country is in this region.
		{Code: "eu-north", Name: "Finland and the Baltics",
			Countries: []string{"FI", "EE", "LV", "LT", "BY"},
			Aliases: map[string][]string{
				StackPhoton:  {"eu"},
				StackGCP:     {"europe-north1"},
				StackAmongUs: {"eu"},
			}},
		// Kazakhstan and Central Asia: the second stop for Russian players.
		{Code: "as-central", Name: "Central Asia",
			Countries: []string{"KZ", "UZ", "KG", "TJ", "TM"},
			Aliases: map[string][]string{
				StackAmongUs: {"as"},
			}},
	}
}

// BuiltinCountryRoutes are the countries that map to an ordered list of
// regions rather than one: places OpenPak takes no nodes, served by their
// neighbours. Each region's own fallback still follows, then home.
func BuiltinCountryRoutes() map[string][]string {
	return map[string][]string{
		"CN": {"ap-east", "ap-northeast"}, // Hong Kong/Taiwan, then Japan/Korea
		"RU": {"eu-north", "as-central"},  // Finland/Baltics, then Kazakhstan
	}
}
