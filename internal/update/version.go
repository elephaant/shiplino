package update

import (
	"strconv"
	"strings"
)

// Version is a parsed semantic version (vMAJOR.MINOR.PATCH[-PRE][+BUILD]).
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // dot-separated prerelease identifiers
}

// ParseVersion parses "v1.2.3", "1.2.3-alpha.2" or "1.2.3+meta".
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		*nums[i] = n
	}
	if hasPre {
		if pre == "" {
			return v, false
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" {
				return v, false
			}
		}
	}
	return v, true
}

// Prerelease reports whether v has a prerelease part (alpha, beta, rc).
func (v Version) Prerelease() bool { return len(v.Pre) > 0 }

// String formats v without the "v" prefix.
func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Compare returns -1, 0 or 1 following semver precedence rules: a
// prerelease sorts before its release, numeric identifiers compare as
// numbers and sort before alphanumeric ones.
func Compare(a, b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c := cmpInt(d[0], d[1]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		var c int
		switch {
		case xerr == nil && yerr == nil:
			c = cmpInt(xn, yn)
		case xerr == nil:
			c = -1
		case yerr == nil:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(a.Pre), len(b.Pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// IsDev reports whether a version string is a local or snapshot build,
// which never updates itself (it didn't come from a release).
func IsDev(s string) bool {
	v, ok := ParseVersion(s)
	if !ok || (v.Major == 0 && v.Minor == 0 && v.Patch == 0) {
		return true
	}
	for _, id := range v.Pre {
		if l := strings.ToLower(id); l == "dev" || strings.HasPrefix(l, "snapshot") {
			return true
		}
	}
	return false
}
