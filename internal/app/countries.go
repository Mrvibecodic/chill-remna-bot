package app

import (
	"context"
	"regexp"
	"strings"
	"time"

	"remnabot/internal/i18n"

	"remnabot/internal/remnawave"
)

// infraCacheTTL bounds how long panel infrastructure (squads + hosts) is cached.
// Infrastructure changes rarely, so a long TTL avoids hitting the panel on every
// buy screen.
const infraCacheTTL = 12 * time.Hour

type infraCacheEntry struct {
	squads    []remnawave.SquadFull
	hosts     []remnawave.Host
	fetchedAt time.Time
}

// infra returns the panel's internal squads and hosts, cached for infraCacheTTL.
// On a panel error it serves the last good cache (if any) so the buy screen
// degrades gracefully rather than dropping the countries line.
func (a *App) infra(ctx context.Context) ([]remnawave.SquadFull, []remnawave.Host) {
	a.infraMu.Lock()
	ce := a.infraCache
	a.infraMu.Unlock()
	if ce != nil && time.Since(ce.fetchedAt) < infraCacheTTL {
		return ce.squads, ce.hosts
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		if ce != nil {
			return ce.squads, ce.hosts
		}
		return nil, nil
	}
	squads, err1 := panel.ListSquadsFull(ctx)
	hosts, err2 := panel.ListHosts(ctx)
	if err1 != nil || err2 != nil {
		if ce != nil {
			return ce.squads, ce.hosts
		}
		return nil, nil
	}
	ne := &infraCacheEntry{squads: squads, hosts: hosts, fetchedAt: time.Now()}
	a.infraMu.Lock()
	a.infraCache = ne
	a.infraMu.Unlock()
	return squads, hosts
}

// planSquadUUIDs resolves the internal-squad UUIDs a plan provisions: the
// per-plan override (Pricing.SquadsInt[months]) else the global plan squads.
func (a *App) planSquadUUIDs(months int) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return nil
	}
	if sq := a.botCfg.Pricing.SquadsInt[months]; len(sq) > 0 {
		return append([]string(nil), sq...)
	}
	return append([]string(nil), a.botCfg.Plan.ActiveInternalSquads...)
}

// country is one distinct destination available to a plan: the flag emoji, its
// ISO-3166-1 alpha-2 code (lowercase, for image rendering on platforms that do
// not draw flag emoji, e.g. Windows webviews), and the display name.
type country struct {
	Flag string
	Code string
	Name string
}

// planCountries returns the distinct countries available to a plan, taken from
// host remarks (e.g. "🇩🇪 Германия"), deduped by flag and in first-seen order,
// plus the count of accessible inbounds (configs).
func (a *App) planCountries(ctx context.Context, months int) (countries []country, inbounds int) {
	squadIDs := a.planSquadUUIDs(months)
	if len(squadIDs) == 0 {
		return nil, 0
	}
	squads, hosts := a.infra(ctx)
	want := map[string]bool{}
	squadSet := map[string]bool{}
	for _, id := range squadIDs {
		squadSet[id] = true
	}
	for _, s := range squads {
		if squadSet[s.UUID] {
			inbounds += s.InboundsCount
			for _, ib := range s.InboundUUIDs {
				want[ib] = true
			}
		}
	}
	if len(want) == 0 {
		return nil, inbounds
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if h.Disabled || h.Hidden || !want[h.InboundUUID] {
			continue
		}
		if anyInSet(h.ExcludedSquads, squadSet) {
			continue
		}
		flag, name := splitFlag(h.Remark)
		if flag == "" || seen[flag] {
			continue
		}
		seen[flag] = true
		countries = append(countries, country{Flag: flag, Code: flagToISO(flag), Name: name})
	}
	return countries, inbounds
}

// flagToISO converts a flag emoji (two regional-indicator runes) to its
// ISO-3166-1 alpha-2 code in lowercase (🇩🇪 -> "de"), or "" if not a flag.
func flagToISO(flag string) string {
	rs := []rune(flag)
	if len(rs) != 2 || !isRegionalIndicator(rs[0]) || !isRegionalIndicator(rs[1]) {
		return ""
	}
	return string([]byte{byte('a' + (rs[0] - 0x1F1E6)), byte('a' + (rs[1] - 0x1F1E6))}) //#nosec G115 -- regional-indicator runes are guarded, offset maps to a..z
}

func anyInSet(list []string, set map[string]bool) bool {
	for _, x := range list {
		if set[x] {
			return true
		}
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// splitFlag extracts the first country flag (a pair of regional-indicator runes)
// from a host remark and returns the flag plus the cleaned country name (trailing
// "#1", "-2", "(3)" host numbering removed). Returns "", "" if no flag present.
func splitFlag(remark string) (flag, name string) {
	rs := []rune(strings.TrimSpace(remark))
	for i := 0; i+1 < len(rs); i++ {
		if isRegionalIndicator(rs[i]) && isRegionalIndicator(rs[i+1]) {
			flag = string(rs[i : i+2])
			rest := strings.TrimSpace(string(rs[:i]) + " " + string(rs[i+2:]))
			return flag, cleanCountryName(rest)
		}
	}
	return "", ""
}

var trailingNum = regexp.MustCompile(`[\s\-#]*\(?\d+\)?$`)

// cleanCountryName trims separators and trailing host numbering ("#1", "-2",
// "(3)") from a remark fragment, leaving just the country label.
func cleanCountryName(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "-–—|·•:#@ ")
	s = trailingNum.ReplaceAllString(strings.TrimSpace(s), "")
	return strings.Join(strings.Fields(s), " ")
}

// countriesLine renders the localized "countries available" block for the chat
// buy screen, or "" when the plan has no detectable countries.
func (a *App) countriesLine(ctx context.Context, lang string, months int) string {
	cs, _ := a.planCountries(ctx, months)
	if len(cs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Name != "" {
			parts = append(parts, c.Flag+" "+c.Name)
		} else {
			parts = append(parts, c.Flag)
		}
	}
	return i18n.T(lang, "buy.countries", len(cs), strings.Join(parts, ", "))
}
