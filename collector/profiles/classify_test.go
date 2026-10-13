package profiles

import (
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// ch30 is the profile list of CH30/DIS-02 Phase 1 (confirmed, E-21).
var ch30 = []string{
	"Cisco Catalyst 9200/9300", "Cisco ISR/ASR", "Cisco ASA/Firepower", "Aruba CX 6xxx",
	"Aruba/HPE ProCurve", "Ubiquiti UniFi Switch/AP", "MikroTik RouterOS", "FortiGate",
	"OPNsense/pfSense", "Dell PowerEdge (iDRAC)", "HPE ProLiant (iLO)", "Lenovo ThinkSystem (XCC)",
	"Supermicro (BMC)", "VMware ESXi", "Proxmox VE", "Synology DSM", "QNAP", "TrueNAS",
	"APC Smart-UPS/NMC", "Eaton UPS", "APC/Raritan PDU", "Windows Server (WMI)", "Linux (SSH)",
}

// TestProfileListCoversCH30 checks that every entry of the confirmed list is
// covered by a classifying profile and that every catalog reference is one
// of the list.
func TestProfileListCoversCH30(t *testing.T) {
	covered := map[string]bool{}
	for _, p := range Default().List() {
		if p.Catalog == "" {
			continue
		}
		if p.CIType == "" {
			t.Errorf("%s covers %q but does not classify", p.Name, p.Catalog)
		}
		covered[p.Catalog] = true
	}
	known := map[string]bool{}
	for _, entry := range ch30 {
		known[entry] = true
		if !covered[entry] {
			t.Errorf("CH30 entry %q has no profile", entry)
		}
	}
	for entry := range covered {
		if !known[entry] {
			t.Errorf("catalog %q is not on the CH30 list", entry)
		}
	}
}

func TestClassificationChain(t *testing.T) {
	r := Default()
	tests := []struct {
		name               string
		obs                Observation
		stage, profile, ci string
	}{
		{"catalyst 9300", Observation{SysObjectID: "1.3.6.1.4.1.9.1.2494", SysDescr: "Cisco IOS Software [Cupertino], Catalyst L3 Switch Software (CAT9K_IOSXE), C9300-48P"}, StageSNMP, "cisco-catalyst-9000-snmp", "switch"},
		{"isr", Observation{SysObjectID: "1.3.6.1.4.1.9.1.2068", SysDescr: "Cisco IOS Software [Bengaluru], ISR Software (X86_64_LINUX_IOSD-UNIVERSALK9-M)"}, StageSNMP, "cisco-isr-asr-snmp", "router"},
		{"asa", Observation{SysObjectID: "1.3.6.1.4.1.9.1.2313", SysDescr: "Cisco Adaptive Security Appliance Version 9.16(3)"}, StageSNMP, "cisco-asa-firepower-snmp", "firewall"},
		{"other cisco", Observation{SysObjectID: "1.3.6.1.4.1.9.1.1208", SysDescr: "Cisco IOS Software, C2960X"}, StageSNMP, "cisco-ios-snmp", "switch"},
		{"aruba cx", Observation{SysObjectID: "1.3.6.1.4.1.47196.4.1.1.1.200", SysDescr: "Aruba JL658A 6300M 24SFP+ 4SFP56 Swch PL.10.10.1000"}, StageSNMP, "aruba-cx-snmp", "switch"},
		{"procurve", Observation{SysObjectID: "1.3.6.1.4.1.11.2.3.7.11.181", SysDescr: "HP J9773A 2530-24G-PoEP Switch"}, StageSNMP, "hpe-procurve-snmp", "switch"},
		{"unifi switch", Observation{SysObjectID: "1.3.6.1.4.1.4413", SysDescr: "USW-24-PoE, 6.6.61.15220"}, StageSNMP, "ubiquiti-unifi-switch-snmp", "switch"},
		{"unifi ap", Observation{SysObjectID: "1.3.6.1.4.1.41112.1.6", SysDescr: "UAP-AC-Pro 6.5.62"}, StageSNMP, "ubiquiti-unifi-snmp", "access_point"},
		{"mikrotik", Observation{SysObjectID: "1.3.6.1.4.1.14988.1", SysDescr: "RouterOS CCR2004-1G-12S+2XS"}, StageSNMP, "mikrotik-routeros-snmp", "router"}, //nolint:misspell // profile name
		{"fortigate", Observation{SysObjectID: "1.3.6.1.4.1.12356.101.1.1004", SysDescr: "FortiGate-100F"}, StageSNMP, "fortinet-fortigate-snmp", "firewall"},
		{"opnsense", Observation{SysObjectID: "1.3.6.1.4.1.8072.3.2.8", SysDescr: "OPNsense.localdomain 13.2-RELEASE-p7 FreeBSD"}, StageSNMP, "opnsense-pfsense", "firewall"},
		{"idrac snmp", Observation{SysObjectID: "1.3.6.1.4.1.674.10892.5", SysDescr: "Dell Out-of-band SNMP Agent for Remote Access Controller"}, StageSNMP, "dell-idrac-redfish", "server"},
		{"esxi", Observation{SysObjectID: "1.3.6.1.4.1.6876.4.1", SysDescr: "VMware ESXi 8.0.2 build-22380479 VMware, Inc. x86_64"}, StageSNMP, "vmware-esxi", "hypervisor"},
		{"proxmox snmp", Observation{SysObjectID: "1.3.6.1.4.1.8072.3.2.10", SysDescr: "Linux pve1 6.8.12-4-pve #1 SMP PREEMPT_DYNAMIC x86_64"}, StageSNMP, "proxmox-ve", "hypervisor"},
		{"linux snmp", Observation{SysObjectID: "1.3.6.1.4.1.8072.3.2.10", SysDescr: "Linux web01 6.1.0-18-amd64 #1 SMP Debian x86_64"}, StageSNMP, "linux-ssh", "server"},
		{"truenas", Observation{SysObjectID: "1.3.6.1.4.1.8072.3.2.10", SysDescr: "TrueNAS-SCALE-24.04.2 Linux nas01"}, StageSNMP, "truenas", "nas"},
		{"synology snmp", Observation{SysObjectID: "1.3.6.1.4.1.6574.1", SysDescr: "Linux DS920"}, StageSNMP, "synology-dsm-snmp", "nas"},
		{"qnap snmp", Observation{SysObjectID: "1.3.6.1.4.1.24681.1"}, StageSNMP, "qnap-qts", "nas"},
		{"apc ups", Observation{SysObjectID: "1.3.6.1.4.1.318.1.3.27", SysDescr: "APC Web/SNMP Management Card (MB:v4.1.0 PF:v6.9.6 PN:apc_hw05_aos)"}, StageSNMP, "apc-ups-snmp", "ups"},
		{"apc pdu", Observation{SysObjectID: "1.3.6.1.4.1.318.1.3.4.5", SysDescr: "APC Rack PDU"}, StageSNMP, "apc-pdu", "pdu"},
		{"raritan pdu", Observation{SysObjectID: "1.3.6.1.4.1.13742.6", SysDescr: "PX3 4.0.20"}, StageSNMP, "raritan-pdu", "pdu"},
		{"eaton ups", Observation{SysObjectID: "1.3.6.1.4.1.534.1", SysDescr: "Eaton 9PX"}, StageSNMP, "eaton-ups", "ups"},
		{"windows snmp", Observation{SysObjectID: "1.3.6.1.4.1.311.1.1.3.1.2", SysDescr: "Hardware: Intel64 Software: Windows Version 6.3"}, StageSNMP, "windows-server-wmi", "server"},
		// Redfish runs only without SNMP evidence.
		{"dell redfish", Observation{Redfish: "Dell Inc. PowerEdge R750"}, StageRedfish, "dell-idrac-redfish", "server"},
		{"hpe redfish", Observation{Redfish: "HPE ProLiant DL380 Gen10"}, StageRedfish, "hpe-ilo-redfish", "server"},
		{"lenovo redfish", Observation{Redfish: "Lenovo ThinkSystem SR650 V2"}, StageRedfish, "lenovo-xcc-redfish", "server"},
		{"supermicro redfish", Observation{Redfish: "Supermicro SYS-1029P-WTR"}, StageRedfish, "supermicro-redfish", "server"},
		// Banners last.
		{"windows wmi", Observation{Banners: []string{"Microsoft Windows Server 2022 Standard"}}, StageBanner, "windows-server-wmi", "server"},
		{"linux ssh", Observation{Banners: []string{"SSH-2.0-OpenSSH_9.2p1 Debian-2", "Linux web01 6.1.0-18-amd64"}}, StageBanner, "linux-ssh", "server"},
		{"proxmox ssh beats linux", Observation{Banners: []string{"SSH-2.0-OpenSSH_9.2p1", "Linux pve1 6.8.12-4-pve"}}, StageBanner, "proxmox-ve", "hypervisor"},
		{"windows openssh", Observation{Banners: []string{"SSH-2.0-OpenSSH_for_Windows_8.1"}}, StageBanner, "windows-server-wmi", "server"},
		{"qnap api", Observation{Banners: []string{"QNAP TS-453D"}}, StageBanner, "qnap-qts", "nas"},
		{"eaton power", Observation{Banners: []string{"EATON 5PX", "ups"}}, StageBanner, "eaton-ups", "ups"},
		{"snmp beats banner", Observation{SysObjectID: "1.3.6.1.4.1.12356.101.1.1004", Banners: []string{"SSH-2.0-OpenSSH_8.0"}}, StageSNMP, "fortinet-fortigate-snmp", "firewall"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := tt.obs
			res := r.Classify(&obs)
			if !res.Classified() {
				t.Fatalf("unclassified, want %s via %s", tt.profile, tt.stage)
			}
			if res.Stage != tt.stage || res.Profile.Name != tt.profile || res.Profile.CIType != tt.ci {
				t.Errorf("got %s/%s/%s, want %s/%s/%s", res.Stage, res.Profile.Name, res.Profile.CIType, tt.stage, tt.profile, tt.ci)
			}
		})
	}
}

func TestUnmatchedDeviceFallsBackToGenericDevice(t *testing.T) {
	rec := plugins.RecordFromResult("sweep", &plugins.Result{
		CIType: "network.endpoint", Name: "10.0.0.9", IP: "10.0.0.9", MAC: "48:8F:5A:00:00:09",
		Attributes: map[string]any{"discoveryMethod": "icmp"},
	}, time.Now())
	res := Default().ClassifyRecord(&rec)
	if res.Classified() || res.Stage != StageFallback {
		t.Fatalf("result = %+v, want fallback", res)
	}
	if rec.Classification.CIType != GenericDevice || rec.Attributes["classified_by"] != StageFallback || rec.Classification.Profile != "" {
		t.Errorf("record = %+v", rec.Classification)
	}
	// The OUI table still names the vendor.
	if rec.Classification.Manufacturer != "MikroTik" {
		t.Errorf("manufacturer = %q, want the OUI vendor", rec.Classification.Manufacturer)
	}
	if v := rec.Validate(); len(v) > 0 {
		t.Errorf("record breaks the contract: %v", v)
	}
}

func TestClassifyRecordMapsTypeVendorModelPluginTemplate(t *testing.T) {
	rec := plugins.RecordFromResult("redfish", &plugins.Result{
		CIType: "bmc", Name: "r750", IP: "10.0.0.20", Manufacturer: "Dell Inc.", Model: "PowerEdge R750",
		Attributes: map[string]any{"api": "redfish"},
	}, time.Now())
	res := Default().ClassifyRecord(&rec)
	if res.Stage != StageRedfish {
		t.Fatalf("stage = %s", res.Stage)
	}
	c := rec.Classification
	if c.CIType != "server" || c.Profile != "dell-idrac-redfish" || c.Manufacturer != "Dell Inc." || c.Model != "PowerEdge R750" {
		t.Errorf("classification = %+v", c)
	}
	if rec.Attributes["plugin"] != "redfish" || rec.Attributes["system_template"] != "server-bmc" || rec.Attributes["classified_by"] != "redfish" {
		t.Errorf("attributes = %v", rec.Attributes)
	}

	snmp := plugins.RecordFromResult("snmp", &plugins.Result{
		CIType: "network.device", Name: "fw", IP: "10.0.0.1",
		Attributes: map[string]any{"sysObjectID": "1.3.6.1.4.1.12356.101.1.1004", "sysDescr": "FortiGate-100F"},
	}, time.Now())
	Default().ClassifyRecord(&snmp)
	if snmp.Classification.CIType != "firewall" || snmp.Classification.Manufacturer != "Fortinet" || snmp.Attributes["system_template"] != "firewall" {
		t.Errorf("snmp classification = %+v %v", snmp.Classification, snmp.Attributes)
	}
}

func TestEveryClassifyingProfileUsesASystemType(t *testing.T) {
	for _, p := range Default().List() {
		if p.CIType == "" {
			continue
		}
		if !SystemCITypes[p.CIType] {
			t.Errorf("%s: %q is not a system CI type", p.Name, p.CIType)
		}
		if p.Plugin == "" || p.SystemTemplate == "" {
			t.Errorf("%s: plugin and system template are required", p.Name)
		}
		if len(p.SysObjectIDs) == 0 && p.SysDescrPattern == "" && p.RedfishPattern == "" && p.BannerPattern == "" {
			t.Errorf("%s: no match criterion", p.Name)
		}
	}
}
