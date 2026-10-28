package notify

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Templates of the notification events (NTF-03, WP-588): every event type
// has a subject and a text in German (de-DE) and English (en-US). A
// recipient gets the text in their language (TemplateLocale); e-mails carry
// the organization's branding (E-91, option 2): its sender name as subject
// prefix and sender, its name in the footer.

// The template locales.
const (
	LocaleDE = "de-DE"
	LocaleEN = "en-US"
)

// legacyUserLocale is the column default of app_user.locale: no language
// chosen, so the organization's locale applies. A user chooses a language
// (de-DE, en-US) or explicitly the organization's locale (empty) in the
// preferences (PUT /notifications/preferences); while the column holds the
// default, a login takes the language of the IdP's locale claim
// (user.PGRepository.RecordLoginLocale, F-383).
const legacyUserLocale = "de"

// TemplateLocale returns the template locale of a recipient: the first of
// the user's locale and the organization's (LOC-01) whose language has
// templates, else de-DE.
func TemplateLocale(userLocale, orgLocale string) string {
	if l := UserLocaleChoice(userLocale); l != "" {
		return l
	}
	if l := templateLanguage(orgLocale); l != "" {
		return l
	}
	return LocaleDE
}

// templateLanguage returns the template locale of a locale's language (de
// → de-DE, en → en-US), empty for any other language.
func templateLanguage(locale string) string {
	lang, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(strings.ReplaceAll(locale, "_", "-"))), "-")
	switch lang {
	case "de":
		return LocaleDE
	case "en":
		return LocaleEN
	}
	return ""
}

// UserLocaleChoice returns the notification language a user chose from the
// stored app_user.locale, as the preferences name it: de-DE or en-US, or
// empty for the organization's locale (the column default de, an explicit
// empty choice, a language without templates).
func UserLocaleChoice(stored string) string {
	if strings.EqualFold(strings.TrimSpace(stored), legacyUserLocale) {
		return ""
	}
	return templateLanguage(stored)
}

// Text is a notification rendered in one language.
type Text struct {
	Subject string
	Body    string
}

// render renders the subject and the text of an event from its data.
type render func(d map[string]any) Text

// Templates are the texts of the events of Subscriptions by event type and
// template locale.
var Templates = map[string]map[string]render{
	events.ReviewCreated: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Neues Review-Item: " + str(d, "kind"),
				"Ein neues Review-Item (" + str(d, "kind") + ") wartet auf eine Entscheidung."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"New review item: " + str(d, "kind"),
				"A new review item (" + str(d, "kind") + ") awaits a decision."}
		},
	},
	events.CollectorOffline: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Collector offline: " + str(d, "name"),
				"Der Collector " + str(d, "name") + " meldet sich seit " + str(d, "last_seen_at") + " nicht mehr."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Collector offline: " + str(d, "name"),
				"Collector " + str(d, "name") + " has not reported since " + str(d, "last_seen_at") + "."}
		},
	},
	events.EntitlementWarning: {
		LocaleDE: func(d map[string]any) Text {
			left := "noch " + fmt.Sprint(d["days_left"]) + " Tage"
			if fmt.Sprint(d["days_left"]) == "1" {
				left = "noch 1 Tag"
			}
			return Text{"Lizenz läuft ab: " + str(d, "feature"),
				"Die Lizenz für " + str(d, "feature") + " läuft am " + str(d, "valid_until") + " ab (" + left + ")."}
		},
		LocaleEN: func(d map[string]any) Text {
			left := fmt.Sprint(d["days_left"]) + " days left"
			if fmt.Sprint(d["days_left"]) == "1" {
				left = "1 day left"
			}
			return Text{"Licence expires: " + str(d, "feature"),
				"The licence of " + str(d, "feature") + " expires on " + str(d, "valid_until") + " (" + left + ")."}
		},
	},
	events.EntitlementExpired: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Lizenz abgelaufen: " + str(d, "feature"),
				"Die Lizenz für " + str(d, "feature") + " ist am " + str(d, "expired_at") + " abgelaufen."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Licence expired: " + str(d, "feature"),
				"The licence of " + str(d, "feature") + " expired on " + str(d, "expired_at") + "."}
		},
	},
	events.InvitationCreated: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Einladung zu Reticora",
				"Sie wurden zu " + str(d, "organization_name") + " eingeladen. Die Einladung gilt bis " + str(d, "expires_at") + ":\n" + str(d, "link")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Invitation to Reticora",
				"You have been invited to " + str(d, "organization_name") + ". The invitation is valid until " + str(d, "expires_at") + ":\n" + str(d, "link")}
		},
	},
	events.ExportCompleted: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Export fertig (" + str(d, "format") + ")",
				"Ihr Export (" + str(d, "format") + ", " + fmt.Sprint(d["row_count"]) + " Zeilen) steht zum Download bereit."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Export ready (" + str(d, "format") + ")",
				"Your export (" + str(d, "format") + ", " + fmt.Sprint(d["row_count"]) + " rows) is ready for download."}
		},
	},
	events.JobFailed: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Job fehlgeschlagen: " + str(d, "kind"),
				"Der Job " + str(d, "kind") + " ist endgültig fehlgeschlagen: " + str(d, "error")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Job failed: " + str(d, "kind"),
				"Job " + str(d, "kind") + " failed for good: " + str(d, "error")}
		},
	},
	events.WebhookDead: {
		// The third dead delivery within 24 hours switches the subscription
		// off (WHK-01).
		LocaleDE: func(d map[string]any) Text {
			if disabled, _ := d["subscription_disabled"].(bool); disabled {
				return Text{"Webhook-Abonnement deaktiviert: " + str(d, "subscription_id"),
					"Das Abonnement " + str(d, "subscription_id") + " wurde nach drei endgültig fehlgeschlagenen Zustellungen innerhalb von 24 Stunden deaktiviert. Letzter Fehler: " + str(d, "last_error")}
			}
			return Text{"Webhook-Zustellung aufgegeben: " + str(d, "event"),
				"Die Zustellung von " + str(d, "event") + " an das Abonnement " + str(d, "subscription_id") + " ist nach " +
					fmt.Sprint(d["attempts"]) + " Versuchen in der Dead-Letter-Queue: " + str(d, "last_error")}
		},
		LocaleEN: func(d map[string]any) Text {
			if disabled, _ := d["subscription_disabled"].(bool); disabled {
				return Text{"Webhook subscription deactivated: " + str(d, "subscription_id"),
					"Subscription " + str(d, "subscription_id") + " was deactivated after three dead deliveries within 24 hours. Last error: " + str(d, "last_error")}
			}
			return Text{"Webhook delivery given up: " + str(d, "event"),
				"The delivery of " + str(d, "event") + " to subscription " + str(d, "subscription_id") + " went to the dead-letter queue after " +
					fmt.Sprint(d["attempts"]) + " attempts: " + str(d, "last_error")}
		},
	},
	events.CertificateExpiring: {
		LocaleDE: func(d map[string]any) Text {
			if str(d, "collector_id") != "" {
				return Text{"Collector-Zertifikat läuft ab: " + str(d, "name"),
					"Das Zertifikat des Collectors " + str(d, "name") + " wurde nicht rechtzeitig erneuert und läuft am " + str(d, "expires_at") + " ab."}
			}
			return Text{"Zertifikat läuft ab: " + str(d, "name"),
				"Das Zertifikat " + str(d, "name") + " läuft am " + str(d, "expires_at") + " ab."}
		},
		LocaleEN: func(d map[string]any) Text {
			if str(d, "collector_id") != "" {
				return Text{"Collector certificate expires: " + str(d, "name"),
					"The certificate of collector " + str(d, "name") + " was not renewed in time and expires on " + str(d, "expires_at") + "."}
			}
			return Text{"Certificate expires: " + str(d, "name"),
				"Certificate " + str(d, "name") + " expires on " + str(d, "expires_at") + "."}
		},
	},
	events.APIKeyCreatorDeactivated: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Service-Account-Keys rotieren: Ersteller deaktiviert",
				"Der Benutzer " + str(d, "user_id") + ", der die folgenden API-Keys für Service-Accounts angelegt hat, wurde deaktiviert. Die Keys bleiben gültig; bitte rotieren:\n" +
					keptKeys(d, "Service-Account")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Rotate service-account keys: creator deactivated",
				"User " + str(d, "user_id") + ", who created the following service-account API keys, was deactivated. The keys stay valid; please rotate them:\n" +
					keptKeys(d, "service account")}
		},
	},
}

// Render renders the notification of an event type in a template locale;
// another locale gets the German text.
func Render(eventType, locale string, data map[string]any) Text {
	byLocale := Templates[eventType]
	if r, ok := byLocale[locale]; ok {
		return r(data)
	}
	if r, ok := byLocale[LocaleDE]; ok {
		return r(data)
	}
	return Text{}
}

// keptKeys lists the kept service-account keys of an
// apikey.creator_deactivated event, one per line: prefix, name, service
// account (labelled account).
func keptKeys(d map[string]any, account string) string {
	list, _ := d["keys"].([]any)
	lines := make([]string, 0, len(list))
	for _, item := range list {
		k, _ := item.(map[string]any)
		lines = append(lines, "- "+str(k, "key_prefix")+" ("+str(k, "name")+"), "+account+" "+
			str(k, "service_account_name")+" ["+str(k, "service_account_id")+"]")
	}
	return strings.Join(lines, "\n")
}

// digestText renders the digest of n notifications of a category in a
// window (hourly or daily) with the list of their subjects.
func digestText(locale, category, window string, n int, list string) Text {
	if locale == LocaleEN {
		period := map[string]string{DigestHourly: "of the last hour", DigestDaily: "of the last day"}[window]
		return Text{fmt.Sprintf("Digest %s: %d notifications", category, n),
			fmt.Sprintf("%d notifications (%s) %s:\n%s", n, category, period, list)}
	}
	period := map[string]string{DigestHourly: "der letzten Stunde", DigestDaily: "des letzten Tages"}[window]
	return Text{fmt.Sprintf("Zusammenfassung %s: %d Benachrichtigungen", category, n),
		fmt.Sprintf("%d Benachrichtigungen (%s) %s:\n%s", n, category, period, list)}
}

// Branding is the organization in its e-mails (NTF-03, E-91 option 2).
type Branding struct {
	// OrganizationName is named in the footer.
	OrganizationName string
	// SenderName is the sender's display name and the subject prefix:
	// settings.branding.sender_name, else the organization's name.
	SenderName string
	// Locale is the organization's locale (LOC-01): the language of the
	// recipients without one of their own.
	Locale string
}

// newBranding builds the branding of an organization from its name,
// locale and settings.branding.sender_name. Names are cut to one line of
// at most 100 characters: they go into mail headers.
func newBranding(name, locale, senderName string) *Branding {
	b := &Branding{OrganizationName: oneLine(name), SenderName: oneLine(senderName), Locale: locale}
	if b.SenderName == "" {
		b.SenderName = b.OrganizationName
	}
	return b
}

// oneLine removes control characters and folds white space; the result
// has at most 100 characters.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > 100 {
		s = strings.TrimSpace(string(r[:100]))
	}
	return s
}

// locale returns the organization's locale; without branding none.
func (b *Branding) locale() string {
	if b == nil {
		return ""
	}
	return b.Locale
}

// senderName returns the sender name; without branding none.
func (b *Branding) senderName() string {
	if b == nil {
		return ""
	}
	return b.SenderName
}

// Mail brands the text of an e-mail in a template locale: the sender name
// as subject prefix, the organization in the footer. Without branding the
// text stays.
func (b *Branding) Mail(locale string, t Text) Text {
	if b == nil {
		return t
	}
	if b.SenderName != "" {
		t.Subject = "[" + b.SenderName + "] " + t.Subject
	}
	if b.OrganizationName != "" {
		footer := "Diese Benachrichtigung wurde von Reticora im Auftrag von " + b.OrganizationName + " versendet."
		if locale == LocaleEN {
			footer = "This notification was sent by Reticora on behalf of " + b.OrganizationName + "."
		}
		t.Body = strings.TrimRight(t.Body, "\n") + "\n\n-- \n" + footer
	}
	return t
}
