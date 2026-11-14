package winps

import "encoding/json"

// parseStrings reads PowerShell's ConvertTo-Json output, which is a bare
// string instead of an array when there is exactly one item.
func parseStrings(out []byte) ([]string, error) {
	var list []string
	if err := json.Unmarshal(out, &list); err != nil {
		var one string
		if json.Unmarshal(out, &one) == nil {
			return []string{one}, nil
		}
		if len(out) == 0 {
			return nil, nil
		}
		return nil, err
	}
	return list, nil
}
