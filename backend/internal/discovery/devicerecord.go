package discovery

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// DeviceRecord is the ingest contract between collector and server (DIS-05,
// api/devicerecord.schema.json). Collector and server share the Go type.
type DeviceRecord = plugins.DeviceRecord

// DeviceRecordBatch is the request body of the DeviceRecord contract.
type DeviceRecordBatch = plugins.DeviceRecordBatch

// legacyIngestDeprecation is the Deprecation header (RFC 9745) of the
// collector_id/items contract: deprecated since 2026-10-12, when DeviceRecords
// replaced it (WP-083).
const legacyIngestDeprecation = "@1791763200"

// maxRecordViolations caps the violations listed in one 422 response.
const maxRecordViolations = 20

// maxIngestBody bounds the decompressed ingest body.
const maxIngestBody = 64 << 20

// ingestBody is either a DeviceRecord batch or the deprecated
// collector_id/items request.
type ingestBody struct {
	Records     []DeviceRecord `json:"records"`
	CollectorID string         `json:"collector_id"`
	Items       []IngestItem   `json:"items"`
}

// readIngestItems decodes the ingest body. DeviceRecords are validated and
// turned into ingest items; the old items contract is answered with a
// Deprecation header. It writes the error response and returns false when the
// body is unusable.
func readIngestItems(w http.ResponseWriter, r *http.Request) ([]IngestItem, bool) {
	// Collectors send the body gzip-compressed (Content-Encoding: gzip).
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "gzip body: "+err.Error())
			return nil, false
		}
		defer gz.Close()
		r.Body = io.NopCloser(io.LimitReader(gz, maxIngestBody+1))
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBody)
	var body ingestBody
	if err := api.ReadJSON(r, &body); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		api.WriteError(w, status, http.StatusText(status), err.Error())
		return nil, false
	}
	items, status, msg := body.ingestItems()
	if status != 0 {
		api.WriteError(w, status, http.StatusText(status), msg)
		return nil, false
	}
	if len(body.Items) > 0 {
		w.Header().Set("Deprecation", legacyIngestDeprecation)
	}
	return items, true
}

// ingestItems validates a body and maps it onto ingest items; a refusal
// returns its HTTP status and message. The HTTP ingest and the collectors'
// edge channel share it.
func (body *ingestBody) ingestItems() (items []IngestItem, status int, msg string) {
	// Reserved attribute namespaces are written by reconciliation only
	// (GLO-13): a record or item carrying one is a malformed request.
	var reserved []string
	for i := range body.Records {
		if err := ci.CheckReservedKeys(fmt.Sprintf("records[%d].attributes", i), body.Records[i].Attributes); err != nil {
			reserved = append(reserved, err.Error())
		}
	}
	for i := range body.Items {
		if err := ci.CheckReservedKeys(fmt.Sprintf("items[%d].attributes", i), body.Items[i].Attributes); err != nil {
			reserved = append(reserved, err.Error())
		}
	}
	if len(reserved) > 0 {
		return nil, http.StatusBadRequest, strings.Join(reserved, "; ")
	}
	switch {
	case len(body.Records) > 0 && len(body.Items) > 0:
		return nil, http.StatusBadRequest, "send either records or items, not both"
	case len(body.Records) > 0:
		var violations []string
		items = make([]IngestItem, 0, len(body.Records))
		for i := range body.Records {
			for _, v := range body.Records[i].Validate() {
				violations = append(violations, fmt.Sprintf("records[%d].%s", i, v))
			}
			items = append(items, itemFromRecord(&body.Records[i]))
		}
		if len(violations) > 0 {
			if len(violations) > maxRecordViolations {
				violations = append(violations[:maxRecordViolations], fmt.Sprintf("and %d more", len(violations)-maxRecordViolations))
			}
			return nil, http.StatusUnprocessableEntity, strings.Join(violations, "; ")
		}
		return items, 0, ""
	case len(body.Items) > 0:
		return body.Items, 0, ""
	}
	return nil, http.StatusBadRequest, "records cannot be empty"
}

// itemFromRecord maps a DeviceRecord onto the ingest item that
// reconciliation works on.
func itemFromRecord(rec *DeviceRecord) IngestItem {
	name := firstNonEmpty(rec.System.Name, firstNonEmpty(rec.Identity.Hostname, rec.TargetIP))
	item := IngestItem{
		CITypeName:   rec.Classification.CIType,
		Name:         name,
		Manufacturer: rec.Classification.Manufacturer,
		Model:        rec.Classification.Model,
		SerialNumber: rec.Identity.SerialNumber,
		ManagementIP: rec.TargetIP,
		Source:       rec.Source,
		HardwareUUID: rec.Identity.HardwareUUID,
		PrimaryMAC:   rec.Identity.PrimaryMAC,
		Hostname:     rec.Identity.Hostname,
		FQDN:         rec.Identity.FQDN,
		SysObjectID:  rec.Classification.SysObjectID,

		RecordID:   rec.RecordID,
		ObservedAt: rec.ObservedAt.UTC(),
		VRFID:      rec.VRFID,
		ScopeID:    rec.ScopeID,
		SiteID:     rec.SiteID,
		ClientID:   rec.ClientID,
	}

	fingerprint := map[string]any{}
	if rec.Identity.HardwareUUID != "" {
		fingerprint["hardware_uuid"] = rec.Identity.HardwareUUID
	}
	var macs []any
	if rec.Identity.PrimaryMAC != "" {
		macs = append(macs, rec.Identity.PrimaryMAC)
	}
	for _, ifc := range rec.Interfaces {
		if ifc.MAC != "" && !strings.EqualFold(ifc.MAC, rec.Identity.PrimaryMAC) {
			macs = append(macs, ifc.MAC)
		}
	}
	if len(macs) > 0 {
		fingerprint["mac_addresses"] = macs
	}
	item.Fingerprint = fingerprint

	// raw_data holds what describes the device; record_id and observed_at
	// change with every record and live in the ingest ledger and the field
	// provenance, so a sighting alone changes no attribute (AUD-03).
	raw := map[string]any{
		"target_ip": rec.TargetIP,
	}
	for key, value := range map[string]string{
		"sys_object_id": rec.Classification.SysObjectID,
		"profile":       rec.Classification.Profile,
		"sys_descr":     rec.System.Description,
	} {
		if value != "" {
			raw[key] = value
		}
	}
	if rec.VRFID != "" {
		raw["vrf_id"] = rec.VRFID
	}
	if rec.System.UptimeSeconds > 0 {
		raw["uptime_seconds"] = rec.System.UptimeSeconds
	}
	if rec.Power != nil {
		raw["power"] = rec.Power
	}
	item.RawData = raw

	attributes := map[string]any{}
	for k, v := range rec.Attributes {
		attributes[k] = v
	}
	for key, value := range map[string]string{
		"os_name":          rec.System.OSName,
		"os_version":       rec.System.OSVersion,
		"firmware_version": rec.System.FirmwareVersion,
	} {
		if value != "" {
			attributes[key] = value
		}
	}
	if len(attributes) > 0 {
		item.Attributes = attributes
	}

	for _, ifc := range rec.Interfaces {
		out := wire.IngestInterface{
			Name: ifc.Name, IfIndex: ifc.IfIndex, MAC: ifc.MAC, SpeedMbps: ifc.SpeedMbps, MTU: ifc.MTU,
			AdminStatus: ifc.AdminStatus, OperStatus: ifc.OperStatus, VlanID: ifc.VlanID,
		}
		for _, ip := range ifc.IPs {
			address := ip.Address
			if ip.PrefixLength > 0 {
				address += "/" + strconv.Itoa(ip.PrefixLength)
			}
			out.IPs = append(out.IPs, address)
			if ip.Address == rec.TargetIP {
				out.Management = true
			}
		}
		item.Interfaces = append(item.Interfaces, out)
	}
	for _, n := range rec.Neighbors {
		item.Relationships = append(item.Relationships, wire.IngestRelationship{
			TargetIP: n.RemoteIP, TargetMAC: n.RemoteMAC, TargetHostname: n.RemoteHostname, TypeKey: "connected_to",
			Protocol: n.Protocol, LocalInterface: n.LocalInterface,
		})
	}
	return item
}
