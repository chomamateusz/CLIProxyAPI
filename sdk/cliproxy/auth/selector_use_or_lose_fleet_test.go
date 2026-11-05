package auth

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

const fleetFable = "claude-fable-5-1"
const fleetOther = "claude-opus-5-5"

type fleetAccount struct {
	id, email    string
	weekday      time.Weekday
	hour, minute int
	short        bool
}

var fleetAccounts = []fleetAccount{
	{"devp-one", "one+claude@devp.it", time.Sunday, 17, 0, false},
	{"amazing", "kontakt@amazingdesign", time.Monday, 20, 0, true},
	{"devp-two", "two+claude@devp.it", time.Tuesday, 12, 0, false},
	{"codex", "", time.Wednesday, 9, 42, false},
}

func fleetTime(t *testing.T, date string) time.Time {
	t.Helper()
	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", date, location)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func fleetReset(account fleetAccount, at time.Time) time.Time {
	reset := time.Date(at.Year(), at.Month(), at.Day(), account.hour, account.minute, 0, 0, at.Location())
	reset = reset.AddDate(0, 0, (int(account.weekday)-int(at.Weekday())+7)%7)
	if !reset.After(at) {
		reset = reset.AddDate(0, 0, 7)
	}
	return reset
}

func fleetAuth(account fleetAccount, at time.Time, weekly, fable, short float64) *Auth {
	reset := fleetReset(account, at)
	if account.id == "codex" {
		return &Auth{ID: account.id, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"plan_type": "pro"}, Quota: QuotaState{ObservedAt: at, Signals: map[string]string{
			"X-Codex-Secondary-Window-Minutes": "10080",
			"X-Codex-Secondary-Used-Percent":   fmt.Sprint(weekly * 100),
			"X-Codex-Secondary-Reset-At":       reset.Format(time.RFC3339),
			"X-Codex-Credits-Has-Credits":      "true",
			"X-Codex-Credits-Balance":          "25",
		}}}
	}
	auth := useOrLoseTestAuth(account.id, at, reset.Sub(at), weekly)
	auth.Status = StatusActive
	auth.Metadata = map[string]any{"email": account.email, "plan_type": "ultra", "email_verified": account.id == "devp-one"}
	useOrLoseTestWindow(auth, "7d_oi", reset, fable)
	if account.short {
		useOrLoseTestWindow(auth, "5h", at.Add(5*time.Hour), short)
	}
	return auth
}

func fleetClaudeAuths(at time.Time, thirdShort bool) []*Auth {
	auths := make([]*Auth, 3)
	for i, account := range fleetAccounts[:3] {
		if i == 2 {
			account.short = thirdShort
		}
		auths[i] = fleetAuth(account, at, .2, .2, .2)
	}
	return auths
}

func fleetWant(auths []*Auth, model string, now time.Time, cap float64) (string, string) {
	room := make([]*Auth, 0, len(auths))
	cappedOnly := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		weekly, _ := useOrLoseClaudeWindow([]QuotaState{auth.Quota}, "7d")
		fable, _ := useOrLoseClaudeWindow([]QuotaState{auth.Quota}, "7d_oi")
		short, _ := useOrLoseClaudeWindow([]QuotaState{auth.Quota}, "5h")
		weeklyLimited := weekly.reset.After(now) && (weekly.utilisation >= 1 || weekly.rejected)
		fableLimited := strings.Contains(model, "fable") && fable.usable(now) && (fable.utilisation >= 1 || fable.rejected)
		if weeklyLimited || fableLimited {
			continue
		}
		if short.hasUsage && short.reset.After(now) && short.utilisation >= cap {
			cappedOnly = append(cappedOnly, auth)
		} else {
			room = append(room, auth)
		}
	}
	reason := "earliest Warsaw weekly reset among credentials with room in all applicable windows"
	if len(room) == 0 {
		room = cappedOnly
		reason = "five-hour capped credentials with weekly capacity precede weekly-limited credentials"
	}
	if len(room) == 0 {
		room = auths
		reason = "all credentials weekly-limited; deterministic earliest-week fallback rather than exclusion"
	}
	best := room[0]
	for _, candidate := range room[1:] {
		if fleetReset(fleetAccounts[fleetIndex(candidate.ID)], now).Before(fleetReset(fleetAccounts[fleetIndex(best.ID)], now)) {
			best = candidate
		}
	}
	return best.ID, reason
}

func fleetIndex(id string) int {
	for i, account := range fleetAccounts {
		if account.id == id {
			return i
		}
	}
	panic("unknown fleet account: " + id)
}

func TestFleetUseOrLoseBarMatrix(t *testing.T) {
	now := fleetTime(t, "2026-11-02 00:00")
	states := []struct {
		name string
		used float64
	}{{"empty", 0}, {"partial", .4}, {"cap-minus-one", .94}, {"at-cap", .95}, {"full", 1}}
	for _, thirdShort := range []bool{false, true} {
		for target := range 3 {
			shortStates := states
			if target == 0 || target == 2 && !thirdShort {
				shortStates = states[:1]
			}
			for _, weekly := range states {
				for _, fable := range states {
					for _, short := range shortStates {
						for _, peerA := range []string{"room", "weekly-full", "short-cap"} {
							for _, peerB := range []string{"room", "weekly-full", "short-cap"} {
								for _, model := range []string{fleetOther, fleetFable} {
									name := fmt.Sprintf("third-5h=%t/%s/7d=%s/7d_oi=%s/5h=%s/peers=%s,%s/%s", thirdShort, fleetAccounts[target].id, weekly.name, fable.name, short.name, peerA, peerB, model)
									t.Run(name, func(t *testing.T) {
										auths := fleetClaudeAuths(now, thirdShort)
										useOrLoseTestWindow(auths[target], "7d", fleetReset(fleetAccounts[target], now), weekly.used)
										useOrLoseTestWindow(auths[target], "7d_oi", fleetReset(fleetAccounts[target], now), fable.used)
										if target == 1 || target == 2 && thirdShort {
											useOrLoseTestWindow(auths[target], "5h", now.Add(5*time.Hour), short.used)
										}
										peer := 0
										for i, auth := range auths {
											if i == target {
												continue
											}
											state := []string{peerA, peerB}[peer]
											peer++
											switch state {
											case "weekly-full":
												useOrLoseTestWindow(auth, "7d", fleetReset(fleetAccounts[i], now), 1)
												useOrLoseTestWindow(auth, "7d_oi", fleetReset(fleetAccounts[i], now), 1)
											case "short-cap":
												if i == 1 || i == 2 && thirdShort {
													useOrLoseTestWindow(auth, "5h", now.Add(5*time.Hour), .95)
												}
											}
										}
										want, reason := fleetWant(auths, model, now, .95)
										got, err := (&UseOrLoseSelector{ShortWindowCapPct: 95}).pick(context.Background(), "claude", model, auths, now)
										if err != nil || got == nil || got.ID != want {
											t.Fatalf("got %s, %v; want %s: %s", fleetPickedID(got), err, want, reason)
										}
									})
								}
							}
						}
					}
				}
			}
		}
	}
}

func fleetPickedID(auth *Auth) string {
	if auth == nil {
		return "<none>"
	}
	return auth.ID
}

func TestFleetUseOrLoseFableFallback(t *testing.T) {
	now := fleetTime(t, "2026-11-02 00:00")
	for _, tc := range []struct {
		name         string
		change       func(*Auth)
		want, reason string
	}{
		{"own-week-exhausted", func(a *Auth) { useOrLoseTestWindow(a, "7d_oi", fleetReset(fleetAccounts[1], now), 1) }, "devp-two", "Fable quota excludes an exhausted account even when 7d has room"},
		{"all-model-week-exhausted", func(a *Auth) { useOrLoseTestWindow(a, "7d", fleetReset(fleetAccounts[1], now), 1) }, "devp-two", "Fable cannot bypass the shared all-model weekly limit"},
		{"missing", func(a *Auth) {
			delete(a.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			delete(a.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Reset")
		}, "amazing", "missing Fable sample falls back to fresh 7d"},
		{"expired-reset", func(a *Auth) { useOrLoseTestWindow(a, "7d_oi", now, 1) }, "amazing", "expired Fable window falls back to 7d"},
		{"stale-observation", func(a *Auth) {
			old := QuotaState{ObservedAt: now.Add(-7 * 24 * time.Hour), Signals: map[string]string{"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "1", "Anthropic-Ratelimit-Unified-7d_oi-Reset": now.Add(time.Hour).Format(time.RFC3339)}}
			delete(a.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			delete(a.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Reset")
			a.ModelStates = map[string]*ModelState{fleetFable: {Quota: old}}
		}, "amazing", "seven-day-old Fable observation falls back to current 7d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auths := fleetClaudeAuths(now, true)
			tc.change(auths[1])
			got, err := (&UseOrLoseSelector{}).pick(context.Background(), "claude", fleetFable, auths, now)
			if err != nil || got == nil || got.ID != tc.want {
				t.Fatalf("got %s, %v; want %s: %s", fleetPickedID(got), err, tc.want, tc.reason)
			}
		})
	}
}

func TestFleetUseOrLoseRealNightRegression(t *testing.T) {
	for _, at := range []string{"2026-11-04 23:00", "2026-11-05 00:00", "2026-11-05 03:00"} {
		for _, used := range []float64{0, .3, .6, .9, .94, .95, 1} {
			t.Run(fmt.Sprintf("%s/amazing-5h=%g", at, used), func(t *testing.T) {
				now := fleetTime(t, at)
				auths := fleetClaudeAuths(now, true)
				for _, name := range []string{"7d", "7d_oi"} {
					useOrLoseTestWindow(auths[0], name, fleetReset(fleetAccounts[0], now), 1)
					auths[0].Quota.Signals["Anthropic-Ratelimit-Unified-"+name+"-Status"] = "rejected"
				}
				useOrLoseTestWindow(auths[1], "5h", now.Add(time.Hour), used)
				want := "amazing"
				if used >= .95 {
					want = "devp-two"
				}
				got, err := (&UseOrLoseSelector{}).pick(context.Background(), "claude", fleetFable, auths, now)
				if err != nil || got == nil || got.ID != want {
					t.Fatalf("got %s, %v; want %s: rejected devp-one is not spare capacity; amazing has room only below 95%%", fleetPickedID(got), err, want)
				}
			})
		}
	}
}

func TestFleetUseOrLoseRealNightTwoAccounts(t *testing.T) {
	for _, at := range []string{"2026-11-04 23:00", "2026-11-05 00:00", "2026-11-05 03:00"} {
		for _, model := range []string{fleetFable, fleetOther} {
			for _, used := range []float64{.94, .95, .96, 1} {
				t.Run(fmt.Sprintf("%s/%s/amazing-5h=%g", at, model, used), func(t *testing.T) {
					now := fleetTime(t, at)
					auths := fleetClaudeAuths(now, false)[:2]
					for _, name := range []string{"7d", "7d_oi"} {
						useOrLoseTestWindow(auths[0], name, fleetReset(fleetAccounts[0], now), 1)
						auths[0].Quota.Signals["Anthropic-Ratelimit-Unified-"+name+"-Status"] = "rejected"
					}
					useOrLoseTestWindow(auths[1], "5h", now.Add(time.Hour), used)
					want, reason := fleetWant(auths, model, now, .95)
					if want != "amazing" {
						t.Fatalf("oracle = %s; want amazing: %s", want, reason)
					}
					got, err := (&UseOrLoseSelector{}).pick(context.Background(), "claude", model, auths, now)
					if err != nil || got == nil || got.ID != want {
						t.Fatalf("got %s, %v; want amazing: a five-hour cap must not prefer a rejected weekly account", fleetPickedID(got), err)
					}
				})
			}
		}
	}
}

func TestFleetUseOrLoseCapConfig(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		used          float64
		want          string
	}{
		{"missing-below", "", .94, "amazing"}, {"missing-at", "", .95, "devp-two"},
		{"percent-below", "short-window-cap-pct: 95", .94, "amazing"}, {"percent-at", "short-window-cap-pct: 95", .95, "devp-two"},
		{"fraction-below", "short-window-cap-pct: 0.95", .94, "amazing"}, {"fraction-at", "short-window-cap-pct: 0.95", .95, "devp-two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config.Config
			input := "routing:\n  strategy: use-or-lose\n  session-affinity: true\n  use-or-lose:\n    " + tc.setting + "\nsave-cooldown-status: false\n"
			if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
				t.Fatal(err)
			}
			if !cfg.Routing.SessionAffinity || cfg.SaveCooldownStatus {
				t.Fatal("fleet config lost affinity or enabled cooldown persistence")
			}
			now := fleetTime(t, "2026-11-02 00:00")
			auths := fleetClaudeAuths(now, true)
			useOrLoseTestWindow(auths[1], "5h", now.Add(time.Hour), tc.used)
			got, err := (&UseOrLoseSelector{ShortWindowCapPct: cfg.Routing.UseOrLose.EffectiveShortWindowCapPct()}).pick(context.Background(), "claude", fleetOther, auths, now)
			if err != nil || got == nil || got.ID != tc.want {
				t.Fatalf("got %s, %v; want %s: 95 and 0.95 must represent the same 95%% boundary, missing defaults to 95", fleetPickedID(got), err, tc.want)
			}
		})
	}
}

type fleetSimulationAccount struct {
	account              fleetAccount
	auth                 *Auth
	reset, shortReset    time.Time
	weekly, fable, short float64
	opportunities        int
	shortCapped          bool
}

func (a *fleetSimulationAccount) canServe(fable bool) bool {
	return (a.account.id == "codex" || a.weekly < 1-1e-9 && (!fable || a.fable < 1-1e-9)) && (!a.account.short || a.short < .95)
}

func (a *fleetSimulationAccount) publish(now time.Time) {
	a.auth.Quota.ObservedAt = now
	if a.account.id == "codex" {
		a.auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = fmt.Sprint(a.weekly * 100)
		a.auth.Quota.Signals["X-Codex-Secondary-Reset-At"] = a.reset.Format(time.RFC3339)
		return
	}
	useOrLoseTestWindow(a.auth, "7d", a.reset, a.weekly)
	useOrLoseTestWindow(a.auth, "7d_oi", a.reset, a.fable)
	if a.account.short {
		useOrLoseTestWindow(a.auth, "5h", a.shortReset, a.short)
	}
}

func TestFleetUseOrLoseWeekSimulation(t *testing.T) {
	for _, thirdShort := range []bool{false, true} {
		for _, fable := range []bool{false, true} {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("third-5h=%t/fable=%t/mixed=%t", thirdShort, fable, mixed), func(t *testing.T) {
					start := fleetTime(t, "2026-11-02 00:00")
					end := fleetTime(t, "2026-11-09 23:00")
					var accounts []*fleetSimulationAccount
					var auths []*Auth
					for i, account := range fleetAccounts {
						if i == 3 && !mixed {
							continue
						}
						if i == 2 {
							account.short = thirdShort
						}
						auth := fleetAuth(account, start, 0, 0, 0)
						accounts = append(accounts, &fleetSimulationAccount{account: account, auth: auth, reset: fleetReset(account, start), shortReset: start.Add(5 * time.Hour)})
						auths = append(auths, auth)
					}
					selector := &UseOrLoseSelector{ShortWindowCapPct: 95}
					model := fleetOther
					if fable {
						model = fleetFable
					}
					provider := "claude"
					if mixed {
						provider = "mixed"
					}
					served := map[string]int{}
					resets, capHits, capClears, hours, violations := 0, 0, 0, 0, 0
					firstViolation := ""
					for now := start; !now.After(end); now = now.Add(time.Hour) {
						hours++
						for _, a := range accounts {
							if !a.reset.After(now) {
								if a.account.id != "codex" && a.weekly < 1-1e-9 && a.opportunities > 0 {
									t.Errorf("%s lost %.2f at %s despite %d requests other accounts served while it had room", a.account.id, 1-a.weekly, a.reset, a.opportunities)
								}
								a.weekly, a.fable, a.opportunities = 0, 0, 0
								a.reset = fleetReset(a.account, now)
								resets++
							}
							if a.account.short && !a.shortReset.After(now) {
								if a.shortCapped {
									capClears++
								}
								a.short, a.shortCapped = 0, false
								for !a.shortReset.After(now) {
									a.shortReset = a.shortReset.Add(5 * time.Hour)
								}
							}
							a.publish(now)
						}
						for range 10 {
							var eligible []*Auth
							for _, a := range accounts {
								if a.canServe(fable) {
									eligible = append(eligible, a.auth)
								}
							}
							if len(eligible) == 0 {
								continue
							}
							got, err := selector.pick(context.Background(), provider, model, auths, now)
							if err != nil || got == nil {
								t.Fatalf("%s: selection failed with room available: %v", now, err)
							}
							var chosen *fleetSimulationAccount
							for _, a := range accounts {
								if a.auth.ID == got.ID {
									chosen = a
								}
							}
							if chosen == nil {
								t.Fatalf("selection returned a credential outside the fleet: %s", got.ID)
							}
							if !chosen.canServe(fable) {
								violations++
								if firstViolation == "" {
									firstViolation = fmt.Sprintf("%s: got %s despite usable alternatives; Claude limits must apply, exhausted Codex with credits must stay usable", now, got.ID)
								}
							}
							for _, a := range accounts {
								if a != chosen && a.canServe(fable) {
									a.opportunities++
								}
							}
							chosen.weekly = math.Min(1, chosen.weekly+.05)
							if fable && chosen.account.id != "codex" {
								chosen.fable = math.Min(1, chosen.fable+.05)
							}
							if chosen.account.short {
								chosen.short = math.Min(1, chosen.short+.19)
								if chosen.short >= .95 && !chosen.shortCapped {
									capHits++
									chosen.shortCapped = true
								}
							}
							chosen.publish(now)
							served[chosen.auth.ID]++
						}
					}
					if hours != 192 || resets < 4 || capHits == 0 || capClears == 0 {
						t.Errorf("hours=%d resets=%d cap-hits=%d clears=%d; want 192 hourly ticks, all weekly resets and exercised short caps", hours, resets, capHits, capClears)
					}
					for _, a := range accounts {
						if served[a.auth.ID] == 0 {
							t.Errorf("%s starved across the week", a.auth.ID)
						}
					}
					if violations != 0 {
						t.Errorf("%d invalid selections across %d hourly ticks; first: %s", violations, hours, firstViolation)
					}
					t.Logf("served=%v weekly-resets=%d cap-hits=%d cap-clears=%d", served, resets, capHits, capClears)
				})
			}
		}
	}
}
