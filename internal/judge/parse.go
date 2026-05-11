// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package judge

import "encoding/json"

// ExtractFirstJSON finds the first balanced JSON object in raw text
// and decodes it into v. Tolerant of code fences, pre/post chatter, or
// trailing whitespace — judge models sometimes wrap their JSON output
// in prose even when instructed not to.
//
// Returns an error only if no JSON object is found, or if the matched
// object fails to decode into v. A successful return guarantees v is
// populated.
func ExtractFirstJSON(text string, v any) error {
	start := -1
	depth := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '{' {
			if start < 0 {
				start = i
			}
			depth++
		} else if c == '}' && start >= 0 {
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(text[start:i+1]), v)
			}
		}
	}
	return errNoJSON
}

var errNoJSON = errString("no JSON object found in judge output")

type errString string

func (e errString) Error() string { return string(e) }
