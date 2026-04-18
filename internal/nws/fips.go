package nws

// fipsStateCodes maps the 2-digit ANSI/FIPS state code (used as the leading
// 2 digits of NWS SAME 6-digit codes) to the USPS 2-letter state abbreviation.
//
// Source: https://www.census.gov/library/reference/code-lists/ansi.html
// SAME format reference: https://www.weather.gov/nwr/coverage
var fipsStateCodes = map[string]string{
	"01": "AL", "02": "AK", "04": "AZ", "05": "AR", "06": "CA",
	"08": "CO", "09": "CT", "10": "DE", "11": "DC", "12": "FL",
	"13": "GA", "15": "HI", "16": "ID", "17": "IL", "18": "IN",
	"19": "IA", "20": "KS", "21": "KY", "22": "LA", "23": "ME",
	"24": "MD", "25": "MA", "26": "MI", "27": "MN", "28": "MS",
	"29": "MO", "30": "MT", "31": "NE", "32": "NV", "33": "NH",
	"34": "NJ", "35": "NM", "36": "NY", "37": "NC", "38": "ND",
	"39": "OH", "40": "OK", "41": "OR", "42": "PA", "44": "RI",
	"45": "SC", "46": "SD", "47": "TN", "48": "TX", "49": "UT",
	"50": "VT", "51": "VA", "53": "WA", "54": "WV", "55": "WI",
	"56": "WY",
	// Territories
	"60": "AS", "66": "GU", "69": "MP", "72": "PR", "78": "VI",
}

// validStateAbbrevs is the set of USPS 2-letter state codes (and territory
// codes) used by NWS. Built once from fipsStateCodes so it can never drift
// from the FIPS table.
var validStateAbbrevs = func() map[string]struct{} {
	out := make(map[string]struct{}, len(fipsStateCodes))
	for _, abbrev := range fipsStateCodes {
		out[abbrev] = struct{}{}
	}
	return out
}()

// IsValidStateCode reports whether the given string is a USPS state (or
// territory) abbreviation recognized by NWS. Used by ingest to validate
// the NWS_AREA env var before sending it upstream — a typo like `ZZ` matches
// the regex shape but isn't a real state and would make the api.weather.gov
// request silently meaningless.
func IsValidStateCode(code string) bool {
	_, ok := validStateAbbrevs[code]
	return ok
}

// StateForSAMECode resolves a 6-digit NWS SAME code (e.g. "055025") to a
// 2-letter state abbreviation. SAME codes are formatted PSSCCC where:
//
//	P   = single-digit county portion indicator (0 = entire county)
//	SS  = ANSI/FIPS state code (2 digits)
//	CCC = ANSI/FIPS county code within the state (3 digits)
//
// So for "055025" the state digits are "55" → WI. See:
// https://www.weather.gov/nwr/coverage
//
// Returns ("", false) if the code is too short or the state digits are not
// recognized. Callers should fall back to area_desc parsing in that case.
func StateForSAMECode(same string) (string, bool) {
	if len(same) < 3 {
		return "", false
	}
	state, ok := fipsStateCodes[same[1:3]]
	return state, ok
}
