package notify

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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

// WorkflowNotification is the template of the workflow action
// send_notification (WP-1095); no event subscription renders it.
const WorkflowNotification = "workflow.notification"

// DirectTemplates are the templates of the direct notifications
// (directCategories), which no subscription renders.
var DirectTemplates = []string{WorkflowNotification}

// Templates are the texts of the events of Subscriptions by event type and
// template locale.
var Templates = map[string]map[string]render{
	// The workflow action send_notification (NTF-05 [P3], WP-1095): subject
	// and text come from the action, the template names the workflow.
	WorkflowNotification: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Workflow: " + str(d, "subject"),
				str(d, "body") + "\n\nGesendet vom Workflow " + str(d, "workflow_name") + " (Lauf " + str(d, "run_id") + ")."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Workflow: " + str(d, "subject"),
				str(d, "body") + "\n\nSent by the workflow " + str(d, "workflow_name") + " (run " + str(d, "run_id") + ")."}
		},
	},
	// Alarms and incidents (WP-987): CI, severity, summary, value and the
	// link (only with a configured frontend); the resolution names the
	// duration.
	events.AlarmOpened: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Alarm " + severityLabel(d, LocaleDE) + ": " + alarmSubject(d),
				"Alarm geöffnet" + onCI(d, LocaleDE) + ".\nSchwere: " + severityLabel(d, LocaleDE) +
					"\nZusammenfassung: " + orDash(str(d, "summary")) + valueLine(d, "Wert") + linkLine(d, "Details")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Alarm " + severityLabel(d, LocaleEN) + ": " + alarmSubject(d),
				"Alarm opened" + onCI(d, LocaleEN) + ".\nSeverity: " + severityLabel(d, LocaleEN) +
					"\nSummary: " + orDash(str(d, "summary")) + valueLine(d, "Value") + linkLine(d, "Details")}
		},
	},
	events.AlarmSeverityChanged: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Alarm eskaliert auf " + severityLabel(d, LocaleDE) + ": " + alarmSubject(d),
				"Alarm eskaliert" + onCI(d, LocaleDE) + ".\nSchwere: " + severityLabel(d, LocaleDE) +
					"\nZusammenfassung: " + orDash(str(d, "summary")) + valueLine(d, "Wert") + linkLine(d, "Details")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Alarm escalated to " + severityLabel(d, LocaleEN) + ": " + alarmSubject(d),
				"Alarm escalated" + onCI(d, LocaleEN) + ".\nSeverity: " + severityLabel(d, LocaleEN) +
					"\nSummary: " + orDash(str(d, "summary")) + valueLine(d, "Value") + linkLine(d, "Details")}
		},
	},
	events.AlarmResolved: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Alarm aufgelöst: " + alarmSubject(d),
				"Alarm aufgelöst" + onCI(d, LocaleDE) + ".\nSchwere: " + severityLabel(d, LocaleDE) +
					"\nZusammenfassung: " + orDash(str(d, "summary")) + valueLine(d, "Wert") +
					"\nDauer: " + alarmDuration(d) + linkLine(d, "Details")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Alarm resolved: " + alarmSubject(d),
				"Alarm resolved" + onCI(d, LocaleEN) + ".\nSeverity: " + severityLabel(d, LocaleEN) +
					"\nSummary: " + orDash(str(d, "summary")) + valueLine(d, "Value") +
					"\nDuration: " + alarmDuration(d) + linkLine(d, "Details")}
		},
	},
	events.IncidentOpened: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Störung " + severityLabel(d, LocaleDE) + incidentOn(d),
				"Störung geöffnet" + onCI(d, LocaleDE) + ".\nSchwere: " + severityLabel(d, LocaleDE) +
					causeLine(d, "Ursache") + linkLine(d, "Details")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Incident " + severityLabel(d, LocaleEN) + incidentOn(d),
				"Incident opened" + onCI(d, LocaleEN) + ".\nSeverity: " + severityLabel(d, LocaleEN) +
					causeLine(d, "Cause") + linkLine(d, "Details")}
		},
	},
	events.IncidentResolved: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Störung aufgelöst" + incidentOn(d),
				"Störung aufgelöst" + onCI(d, LocaleDE) + ".\nSchwere: " + severityLabel(d, LocaleDE) +
					causeLine(d, "Ursache") + linkLine(d, "Details")}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Incident resolved" + incidentOn(d),
				"Incident resolved" + onCI(d, LocaleEN) + ".\nSeverity: " + severityLabel(d, LocaleEN) +
					causeLine(d, "Cause") + linkLine(d, "Details")}
		},
	},
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
	events.MonitoringSuspensionChanged: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Monitoring-Prüfungen pausiert oder fortgesetzt",
				"Wegen einer Änderung des Lizenzumfangs (" + strList(d["features"]) + ") wurden " + fmt.Sprint(d["paused"]) +
					" Prüfungen pausiert und " + fmt.Sprint(d["resumed"]) + " fortgesetzt. Die Einstellungen bleiben erhalten."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Monitoring checks paused or resumed",
				"Because the licensed scope changed (" + strList(d["features"]) + "), " + fmt.Sprint(d["paused"]) +
					" checks were paused and " + fmt.Sprint(d["resumed"]) + " resumed. Their configuration is kept."}
		},
	},
	events.EntitlementChanged: {
		LocaleDE: func(d map[string]any) Text {
			if before := str(d, "plan_before"); before != "" {
				return Text{"Paket geändert: " + str(d, "plan"),
					"Das Paket Ihrer Organisation wurde von " + before + " auf " + str(d, "plan") + " umgestellt. Geändert: " + strList(d["changed"]) + "."}
			}
			return Text{"Lizenzumfang geändert",
				"Der Lizenzumfang Ihrer Organisation (Paket " + str(d, "plan") + ") wurde angepasst. Geändert: " + strList(d["changed"]) + "."}
		},
		LocaleEN: func(d map[string]any) Text {
			if before := str(d, "plan_before"); before != "" {
				return Text{"Plan changed: " + str(d, "plan"),
					"The plan of your organization was changed from " + before + " to " + str(d, "plan") + ". Changed: " + strList(d["changed"]) + "."}
			}
			return Text{"Licence scope changed",
				"The licence scope of your organization (plan " + str(d, "plan") + ") was adjusted. Changed: " + strList(d["changed"]) + "."}
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
	events.OrgDeletionRequested: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Löschung der Organisation beantragt",
				"Die Löschung der Organisation wurde beantragt. Sie wird am " + str(d, "due_at") +
					" endgültig ausgeführt; bis dahin ist die Organisation schreibgeschützt und der Antrag kann widerrufen werden."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Deletion of the organization requested",
				"The deletion of the organization was requested. It is carried out for good at " + str(d, "due_at") +
					"; until then the organization is read-only and the request can be cancelled."}
		},
	},
	events.OrgDeletionCancelled: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Löschung der Organisation widerrufen",
				"Der Antrag auf Löschung der Organisation (fällig am " + str(d, "due_at") + ") wurde widerrufen; die Organisation ist wieder beschreibbar."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Deletion of the organization cancelled",
				"The request to delete the organization (due at " + str(d, "due_at") + ") was cancelled; the organization can be written again."}
		},
	},
	events.OrgExportCompleted: {
		LocaleDE: func(d map[string]any) Text {
			return Text{"Vollexport der Organisation fertig",
				"Der Vollexport der Organisation (" + fmt.Sprint(d["rows"]) + " Zeilen, " + fmt.Sprint(d["bytes"]) +
					" Bytes) steht bis " + str(d, "expires_at") + " zum Download bereit. Der Download verlangt eine Anmeldung mit MFA."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Full export of the organization ready",
				"The full export of the organization (" + fmt.Sprint(d["rows"]) + " rows, " + fmt.Sprint(d["bytes"]) +
					" bytes) can be downloaded until " + str(d, "expires_at") + ". The download needs a sign-in with MFA."}
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
	events.TicketSLABreached: {
		// The missed due time of a ticket (WP-769): number, kind, due time.
		LocaleDE: func(d map[string]any) Text {
			kind := "Reaktionsfrist"
			if str(d, "kind") == "resolution" {
				kind = "Lösungsfrist"
			}
			return Text{"SLA verletzt: Ticket #" + fmt.Sprint(d["ticket_number"]) + " (" + kind + ")",
				"Die " + kind + " von Ticket #" + fmt.Sprint(d["ticket_number"]) + " (Priorität " + str(d, "priority") +
					") war am " + str(d, "due_at") + " fällig und ist überschritten."}
		},
		LocaleEN: func(d map[string]any) Text {
			kind := "response time"
			if str(d, "kind") == "resolution" {
				kind = "resolution time"
			}
			return Text{"SLA breached: ticket #" + fmt.Sprint(d["ticket_number"]) + " (" + kind + ")",
				"The " + kind + " of ticket #" + fmt.Sprint(d["ticket_number"]) + " (priority " + str(d, "priority") +
					") was due at " + str(d, "due_at") + " and has passed."}
		},
	},
	events.ContractExpiring: {
		// A reached stage of a contract (WP-270): number, due date, days
		// left, end date.
		LocaleDE: func(d map[string]any) Text {
			return Text{"Vertrag läuft ab: " + str(d, "number"),
				"Der Vertrag " + str(d, "number") + " endet am " + str(d, "expires_at") + ". Stichtag für die Kündigung ist der " +
					str(d, "due_date") + ", noch " + fmt.Sprint(d["days_left"]) + " Tage."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Contract expiring: " + str(d, "number"),
				"Contract " + str(d, "number") + " ends on " + str(d, "expires_at") + ". The notice deadline is " +
					str(d, "due_date") + ", " + fmt.Sprint(d["days_left"]) + " days left."}
		},
	},
	events.AssetWarrantyExpiring: {
		// A reached stage of an asset warranty (WP-270): name, inventory
		// number, due date, days left.
		LocaleDE: func(d map[string]any) Text {
			return Text{"Garantie läuft ab: " + str(d, "name"),
				"Die Garantie des Assets " + str(d, "name") + " (" + str(d, "inventory_no") + ") endet am " + str(d, "due_date") +
					", noch " + fmt.Sprint(d["days_left"]) + " Tage."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Warranty expiring: " + str(d, "name"),
				"The warranty of asset " + str(d, "name") + " (" + str(d, "inventory_no") + ") ends on " + str(d, "due_date") +
					", " + fmt.Sprint(d["days_left"]) + " days left."}
		},
	},
	events.MaintenanceScheduled: {
		// A maintenance window announced to an affected client (WP-1034):
		// title, time span and the number of affected CIs and services.
		LocaleDE: func(d map[string]any) Text {
			return Text{"Geplante Wartung: " + str(d, "title"),
				"Für den Zeitraum " + str(d, "starts_at") + " bis " + str(d, "ends_at") + " ist die Wartung „" + str(d, "title") +
					"“ geplant. Betroffen sind " + fmt.Sprint(d["affected_cis"]) + " Komponenten und " +
					fmt.Sprint(d["services"]) + " Dienste."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Scheduled maintenance: " + str(d, "title"),
				"The maintenance \"" + str(d, "title") + "\" is scheduled from " + str(d, "starts_at") + " to " + str(d, "ends_at") +
					". It affects " + fmt.Sprint(d["affected_cis"]) + " components and " + fmt.Sprint(d["services"]) + " services."}
		},
	},
	events.StockBelowMinimum: {
		// The total stock of a consumable below its minimum level (WP-285,
		// WP-845): name, SKU, total stock and minimum level with the unit.
		LocaleDE: func(d map[string]any) Text {
			return Text{"Mindestbestand unterschritten: " + str(d, "name"),
				"Der Gesamtbestand des Artikels " + str(d, "name") + skuLabel(d, "Artikelnummer") + " beträgt " +
					quantity(d, "on_hand_total", LocaleDE) + " und liegt unter dem Mindestbestand von " +
					quantity(d, "min_level", LocaleDE) + "."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Stock below minimum: " + str(d, "name"),
				"The total stock of article " + str(d, "name") + skuLabel(d, "SKU") + " is " +
					quantity(d, "on_hand_total", LocaleEN) + ", below the minimum level of " + quantity(d, "min_level", LocaleEN) + "."}
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
	events.DocumentScanned: {
		// Only a finding notifies (Subscriptions): the version is
		// quarantined, its download refused (SEC-09, E-42).
		LocaleDE: func(d map[string]any) Text {
			return Text{"Schadsoftware in Dokument gefunden: " + str(d, "scan_signature"),
				"Die Version " + fmt.Sprint(d["version"]) + " des Dokuments " + str(d, "document_id") + " enthält " +
					str(d, "scan_signature") + " und wurde in Quarantäne gesetzt. Der Download ist gesperrt."}
		},
		LocaleEN: func(d map[string]any) Text {
			return Text{"Malware found in document: " + str(d, "scan_signature"),
				"Version " + fmt.Sprint(d["version"]) + " of document " + str(d, "document_id") + " contains " +
					str(d, "scan_signature") + " and was quarantined. Its download is blocked."}
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

// skuLabel names the SKU of a stock.below_minimum event in parentheses,
// labelled; nothing for an article without SKU.
func skuLabel(d map[string]any, label string) string {
	if sku := str(d, "sku"); sku != "" {
		return " (" + label + " " + sku + ")"
	}
	return ""
}

// quantity renders a quantity of a stock.below_minimum event with the
// article's unit; German texts take a decimal comma (2,5 l).
func quantity(d map[string]any, key, locale string) string {
	n := fmt.Sprint(d[key])
	if v, ok := d[key].(float64); ok {
		n = strconv.FormatFloat(v, 'f', -1, 64)
	}
	if locale == LocaleDE {
		n = strings.Replace(n, ".", ",", 1)
	}
	if unit := str(d, "unit"); unit != "" {
		return n + " " + unit
	}
	return n
}

// strList joins a list of the event data (a JSON array) with commas.
func strList(v any) string {
	var parts []string
	switch list := v.(type) {
	case []any:
		for _, item := range list {
			parts = append(parts, fmt.Sprint(item))
		}
	case []string:
		parts = list
	}
	if len(parts) == 0 {
		return "–"
	}
	return strings.Join(parts, ", ")
}

// severityLabels are the severities of alarms and incidents per locale.
var severityLabels = map[string]map[string]string{
	LocaleDE: {"critical": "kritisch", "warning": "Warnung", "info": "Info"},
	LocaleEN: {"critical": "critical", "warning": "warning", "info": "info"},
}

// severityLabel names the severity of an alarm or incident.
func severityLabel(d map[string]any, locale string) string {
	if l, ok := severityLabels[locale][str(d, "severity")]; ok {
		return l
	}
	return orDash(str(d, "severity"))
}

// alarmSubject is the CI name of an alarm, else its summary, else its id.
func alarmSubject(d map[string]any) string {
	for _, k := range []string{"ci_name", "summary", "id"} {
		if v := oneLine(str(d, k)); v != "" {
			return v
		}
	}
	return "–"
}

// onCI names the CI of an alarm or incident ("on CI x"); nothing without.
func onCI(d map[string]any, locale string) string {
	name := oneLine(str(d, "ci_name"))
	if name == "" {
		return ""
	}
	if locale == LocaleEN {
		return " on " + name
	}
	return " auf " + name
}

// incidentOn is ": <CI name>" of an incident's root CI; nothing without.
func incidentOn(d map[string]any) string {
	if name := oneLine(str(d, "ci_name")); name != "" {
		return ": " + name
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// valueLine is the line of the measured value; nothing without one.
func valueLine(d map[string]any, label string) string {
	v, ok := d["value"].(float64)
	if !ok {
		return ""
	}
	return "\n" + label + ": " + strconv.FormatFloat(v, 'g', -1, 64)
}

// causeLine is the line of the cause class of an incident; nothing without.
func causeLine(d map[string]any, label string) string {
	if c := str(d, "cause_class"); c != "" {
		return "\n" + label + ": " + c
	}
	return ""
}

// linkLine is the line of the link; nothing without a configured frontend.
func linkLine(d map[string]any, label string) string {
	if l := str(d, "link"); l != "" {
		return "\n" + label + ": " + l
	}
	return ""
}

// alarmDuration is the time from opened_at to resolved_at of an alarm,
// rounded to the second; "–" when one is missing.
func alarmDuration(d map[string]any) string {
	opened, err1 := time.Parse(time.RFC3339Nano, str(d, "opened_at"))
	resolved, err2 := time.Parse(time.RFC3339Nano, str(d, "resolved_at"))
	if err1 != nil || err2 != nil || resolved.Before(opened) {
		return "–"
	}
	return resolved.Sub(opened).Round(time.Second).String()
}
