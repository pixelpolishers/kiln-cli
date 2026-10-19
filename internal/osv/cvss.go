package osv

import (
	"fmt"
	"math"
	"strings"
)

// CVSSv3 returns the base score of a CVSS 3.0/3.1 vector such as
// "CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:U/C:L/I:L/A:L". ok is false for anything else
// (CVSS 4.0 vectors included: they need a lookup table this client does not carry).
func CVSSv3(vector string) (score float64, ok bool) {
	parts := strings.Split(vector, "/")
	if len(parts) < 9 || (parts[0] != "CVSS:3.1" && parts[0] != "CVSS:3.0") {
		return 0, false
	}
	m := map[string]string{}
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(p, ":")
		if !found {
			return 0, false
		}
		m[k] = v
	}
	pick := func(key string, table map[string]float64) (float64, bool) {
		v, ok := table[m[key]]
		return v, ok
	}
	changed := m["S"] == "C"
	if m["S"] != "C" && m["S"] != "U" {
		return 0, false
	}
	av, ok1 := pick("AV", map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2})
	ac, ok2 := pick("AC", map[string]float64{"L": 0.77, "H": 0.44})
	pr := map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}
	if changed {
		pr = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.5}
	}
	prv, ok3 := pick("PR", pr)
	ui, ok4 := pick("UI", map[string]float64{"N": 0.85, "R": 0.62})
	cia := map[string]float64{"H": 0.56, "L": 0.22, "N": 0}
	c, ok5 := pick("C", cia)
	i, ok6 := pick("I", cia)
	a, ok7 := pick("A", cia)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) {
		return 0, false
	}
	iss := 1 - (1-c)*(1-i)*(1-a)
	var impact float64
	if changed {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}
	if impact <= 0 {
		return 0, true
	}
	exploit := 8.22 * av * ac * prv * ui
	if changed {
		return roundUp(math.Min(1.08*(impact+exploit), 10)), true
	}
	return roundUp(math.Min(impact+exploit, 10)), true
}

// roundUp rounds up to one decimal, as the CVSS 3.1 specification defines it.
func roundUp(x float64) float64 {
	n := int(math.Round(x * 100000))
	if n%10000 == 0 {
		return float64(n) / 100000
	}
	return float64(n/10000+1) / 10
}

// Rating is the qualitative name of a CVSS v3 score.
func Rating(score float64) string {
	switch {
	case score == 0:
		return "NONE"
	case score < 4:
		return "LOW"
	case score < 7:
		return "MEDIUM"
	case score < 9:
		return "HIGH"
	}
	return "CRITICAL"
}

func scoreText(score float64) string { return fmt.Sprintf("%.1f %s", score, Rating(score)) }
