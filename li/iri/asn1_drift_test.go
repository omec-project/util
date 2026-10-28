// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is the TS 33.128 counterpart of the X1 schema drift audit: it compiles
// each record's field list from the published ASN.1 module and fails when the
// module defines a field this package neither models nor declares as absent on
// purpose.
//
// What it can and cannot establish, because the difference is the whole reason
// it exists:
//
//   - It CAN tell you that the module defines a field and we never populate it.
//     That is the silent half — a mediation function receives a record that
//     decodes cleanly and carries less than it should.
//   - It CANNOT tell you whether that field is mandatory. The M/C/O markers live in
//     the payload tables of TS 33.128 clause 6.2, which are prose and are not
//     machine-readable, and they do not always agree with the module's OPTIONAL
//     markers: gTPTunnelInfo is OPTIONAL in the module and M in three payload
//     tables. So a record can omit a field the tables mark M and decode perfectly
//     against this module; CONFORMANCE.md records where the two disagree.
//
// The judgement therefore stays with the reader: this test enumerates, the
// payload tables decide, and two maps record what was decided —
// declaredAbsent for fields that need not be populated, and
// knownConditionalDefects for fields that should be and are not.

// declaredAbsent lists, per record, the module-defined fields this package does
// not model *and need not*, each with the reason. Fields it SHOULD populate and
// does not live in knownConditionalDefects instead: mixing the two would let a
// real omission be silenced by the same edit that records an inapplicable field.
// A field whose payload table marks it M is rejected from this map outright.
//
// The anti-rot rule is the X1 audit's: a field in neither map fails the test, AND
// an entry that has since been modelled — or that the module no longer defines —
// also fails, so neither list can rot into a description of a state that has moved
// on.
//
// Every entry carries the field's M/C/O marker, the payload table it came from,
// and a verdict on whether this project meets the condition:
//
//	MET        the network function holds the data and does not report it — a
//	           finding, listed in CONFORMANCE.md
//	NOT HELD   the network function does not hold the datum
//	N/A        the condition cannot arise; the feature is not implemented here
//	DEFERRED   the datum exists but the subtree is deliberately not modelled
//	BLOCKED    the codec cannot express the value
//	DEPRECATED the specification no longer uses the field
//	UNTRACED   a similarly named field exists in the context and the mapping was
//	           not followed through — the one verdict that is an admission rather
//	           than a conclusion, and deliberately visible as such
var declaredAbsent = map[string]map[string]string{
	"AMFDeregistration": {
		"additionalUserIdentifiers":    "C, table 6.2.2.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"cause":                        "C, table 6.2.2.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                     "C, table 6.2.2.2.3-1: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"reRegRequiredIndicator":       "C, table 6.2.2.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"switchOffIndicator":           "C, table 6.2.2.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"unavailabilityPeriodDuration": "C, table 6.2.2.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFIdentifierAssociation": {
		"additionalUserIdentifiers": "C, table 6.2.2.2.7-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFIdentifierDeassociation": {
		"additionalUserIdentifiers": "C, table 6.2.2.2.7-2: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                  "C, table 6.2.2.2.7-2: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
	},
	"AMFLocationUpdate": {
		"additionalUserIdentifiers":     "C, table 6.2.2.2.4-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"deprecatedOldGUTI":             "C, table 6.2.2.2.4-1: DEPRECATED: the specification records it as no longer used",
		"deprecatedSMSOverNASIndicator": "C, table 6.2.2.2.4-1: DEPRECATED: the specification records it as no longer used",
		"uEAreaIndication":              "C, table 6.2.2.2.4-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFPositioningInfoTransfer": {
		"additionalUserIdentifiers": "C, table 6.2.2.2.8-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFRANHandoverRequest": {
		"locationReportingRequestType": "C, table 6.2.2.2.9.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"mobilityRestrictionList":      "C, table 6.2.2.2.9.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"nPNAccessInformation":         "C, table 6.2.2.2.9.3-1: N/A: non-public network access is not implemented",
	},
	"AMFRegistration": {
		"additionalUserIdentifiers":       "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"alternativeNSSAI":                "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"eMM5GRegStatus":                  "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"equivalentPLMNList":              "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"establishmentCauseNon3GPPAccess": "C, table 6.2.2.2.2-1: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"fiveGMMCapability":               "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"fiveGSUpdateType":                "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"initialRANUEContextSetup":        "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                        "C, table 6.2.2.2.2-1: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"mACRestIndicator":                "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"mUSIMUERequestType":              "C, table 6.2.2.2.2-1: N/A: multi-USIM is not implemented",
		"nASTransportInitialInformation":  "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"nGInformation":                   "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"non3GPPAccessEndpoint":           "C, table 6.2.2.2.2-1: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"nonIMEISVPEI":                    "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"oldGUTI":                         "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"pagingRestrictionIndicator":      "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"rRCEstablishmentCause":           "C, table 6.2.2.2.2-1: UNTRACED: the AMF holds RAN-supplied UE context; whether the establishment cause survives to the POI was not traced",
		"sMSOverNasIndicator":             "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"sORTransparentContainer":         "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"slice":                           "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uEAreaIndication":                "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"unavailabilityPeriodDuration":    "C, table 6.2.2.2.2-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFStartOfInterceptionWithRegisteredUE": {
		"additionalUserIdentifiers":    "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"eMM5GRegStatus":               "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"fiveGSUpdateType":             "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                     "C, table 6.2.2.2.5-1: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"non3GPPAccessEndpoint":        "C, table 6.2.2.2.5-1: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"oldGUTI":                      "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"sMSOverNASIndicator":          "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"sORTransparentContainer":      "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"slice":                        "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"timeOfRegistration":           "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uEAreaIndication":             "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uEPolicy":                     "C, table 6.2.2.2.5-1: UNTRACED: the AMF holds policy association state; whether it carries the MANAGE UE POLICY COMMAND payload was not traced",
		"unavailabilityPeriodDuration": "C, table 6.2.2.2.5-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFUEServiceAccept": {
		"deprecatedUERequestType": "O, table 6.2.2.2.13-1: DEPRECATED: the specification records it as no longer used",
		"forbiddenTAIList":        "C, table 6.2.2.2.13-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"pDUSessionStatus":        "C, table 6.2.2.2.13-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"pagingRestriction":       "C, table 6.2.2.2.13-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uERequestType":           "C, table 6.2.2.2.13-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uplinkDataStatus":        "C, table 6.2.2.2.13-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"AMFUnsuccessfulProcedure": {
		"additionalUserIdentifiers": "C, table 6.2.2.2.6-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"alternativeNSSAI":          "C, table 6.2.2.2.6-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"requestedSlice":            "C, table 6.2.2.2.6-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"SMFPDUSessionEstablishment": {
		"alternativeNSSAI":              "C, table 6.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"ePS5GSComboInfo":               "C, table 6.2.3-1: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"ePSPDNConnectionEstablishment": "C, table 6.2.3-1: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"gEOSatelliteID":                "C, table 6.2.3-1: N/A: satellite backhaul is not implemented",
		"hSMFURI":                       "C, table 6.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"handoverState":                 "C, table 6.2.3-1: UNTRACED: a similarly named field exists in the SMF context; the mapping was not traced",
		"location":                      "C, table 6.2.3-1: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"non3GPPAccessEndpoint":         "C, table 6.2.3-1: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"oldPDUSessionID":               "C, table 6.2.3-1: UNTRACED: a similarly named field exists in the SMF context; the mapping was not traced",
		"pCCRules":                      "C, table 6.2.3-1: UNTRACED: the SMF holds SmPolicyData; whether it carries the PCC rule set in the reported form was not traced",
		"sMPDUDNRequest":                "C, table 6.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"satelliteBackhaulCategory":     "C, table 6.2.3-1: N/A: satellite backhaul is not implemented",
		"selectedDNN":                   "C, table 6.2.3-1: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uEEPSPDNConnection":            "C, table 6.2.3-1: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
	},
	"SMFPDUSessionModification": {
		"alternativeNSSAI":             "C, table 6.2.3-2: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"ePS5GSComboInfo":              "C, table 6.2.3-2: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"ePSPDNConnectionModification": "C, table 6.2.3-2: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"gEOSatelliteID":               "C, table 6.2.3-2: N/A: satellite backhaul is not implemented",
		"handoverState":                "C, table 6.2.3-2: UNTRACED: a similarly named field exists in the SMF context; the mapping was not traced",
		"location":                     "C, table 6.2.3-2: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"non3GPPAccessEndpoint":        "C, table 6.2.3-2: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"pCCRules":                     "C, table 6.2.3-2: UNTRACED: the SMF holds SmPolicyData; whether it carries the PCC rule set in the reported form was not traced",
		"pFDDataForApp":                "C, table 6.2.3-2: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"satelliteBackhaulCategory":    "C, table 6.2.3-2: N/A: satellite backhaul is not implemented",
		"uPPathChange":                 "C, table 6.2.3-2: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"SMFPDUSessionRelease": {
		"alternativeNSSAI":        "C, table 6.2.3-3: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"cause":                   "C, table 6.2.3-3: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"ePS5GSComboInfo":         "C, table 6.2.3-3: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"ePSPDNConnectionRelease": "C, table 6.2.3-3: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"fiveGMMCause":            "C, table 6.2.3-3: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                "C, table 6.2.3-3: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"nGAPCause":               "C, table 6.2.3-3: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"pCCRuleIDs":              "C, table 6.2.3-3: UNTRACED: as pCCRules",
		"timeOfFirstPacket":       "C, table 6.2.3-3: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"timeOfLastPacket":        "C, table 6.2.3-3: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
	"SMFStartOfInterceptionWithEstablishedPDUSession": {
		"ePS5GSComboInfo": "C, table 6.2.3-4: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"ePSStartOfInterceptionWithEstablishedPDNConnection": "C, table 6.2.3-4: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"gEOSatelliteID":             "C, table 6.2.3-4: N/A: satellite backhaul is not implemented",
		"hSMFURI":                    "C, table 6.2.3-4: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                   "C, table 6.2.3-4: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"non3GPPAccessEndpoint":      "C, table 6.2.3-4: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"pCCRules":                   "C, table 6.2.3-4: UNTRACED: the SMF holds SmPolicyData; whether it carries the PCC rule set in the reported form was not traced",
		"pFDDataForApps":             "C, table 6.2.3-4: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"sMPDUDNRequest":             "C, table 6.2.3-4: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"satelliteBackhaulCategory":  "C, table 6.2.3-4: N/A: satellite backhaul is not implemented",
		"timeOfSessionEstablishment": "C, table 6.2.3-4: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"uEEPSPDNConnection":         "C, table 6.2.3-4: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
	},
	"SMFUnsuccessfulProcedure": {
		"alternativeNSSAI":            "C, table 6.2.3-5: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"ePSPDNUnsuccessfulProcedure": "C, table 6.2.3-5: N/A: reports an EPS/5GS interworking case; this project implements no N26 interworking or SGW/PGW role",
		"hSMFURI":                     "C, table 6.2.3-5: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"location":                    "C, table 6.2.3-5: DEFERRED: li/iri.Location models only locationInfo.currentLocation; the userLocation subtree the table asks for is a documented deferral, so the reportable content does not exist yet",
		"non3GPPAccessEndpoint":       "C, table 6.2.3-5: N/A: requires non-3GPP access (N3IWF/TNGF/TWIF), which this deployment does not provide",
		"requestedSlice":              "C, table 6.2.3-5: NOT HELD: the network function does not hold this datum, so the condition is not met",
		"sMPDUDNRequest":              "C, table 6.2.3-5: NOT HELD: the network function does not hold this datum, so the condition is not met",
	},
}

// knownConditionalDefects is the other half, and the distinction is the point: these
// are fields whose condition this project MEETS, or would meet but for a codec
// limitation, and which it does not populate. They are defects, not dispositions —
// each is a record that decodes cleanly and carries less than TS 33.128 says it
// should, which no decoder can see.
//
// They are listed separately so they can be counted, and TestNoKnownConditionalDefects
// requires the list to be empty. Adding one therefore has to be a deliberate, reviewed act
// rather than another line in a list of things that are fine. The findings themselves are
// written up in CONFORMANCE.md.
var knownConditionalDefects = map[string]map[string]string{
	// Empty: no field whose condition this project meets goes unreported. The list
	// stays so the next such field has an established place to be recorded, and so
	// adding one is a deliberate act that TestNoKnownConditionalDefects makes visible.

}

// modelledTags returns the ASN.1 context tags the Go struct declares.
func modelledTags(t *testing.T, typ reflect.Type) map[int]string {
	t.Helper()
	tags := map[int]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		opts := f.Tag.Get("asn1")
		for _, part := range strings.Split(opts, ",") {
			if !strings.HasPrefix(part, "tag:") {
				continue
			}
			n, err := strconv.Atoi(strings.TrimPrefix(part, "tag:"))
			if err != nil {
				t.Fatalf("%s.%s: unparsable asn1 tag %q", typ.Name(), f.Name, part)
			}
			tags[n] = f.Name
		}
	}
	return tags
}

// TestASN1RecordDrift fails when the published module defines a field for a
// record this project emits that the record neither models nor declares absent.
func TestASN1RecordDrift(t *testing.T) {
	module := moduleSequences(t)

	var undeclared []string
	var staleDeclarations []string

	for name := range goldenSamples() {
		fields, ok := module[name]
		if !ok {
			t.Errorf("record %s has no SEQUENCE in %s — either the record name is wrong "+
				"or the module no longer defines it", name, modulePath)
			continue
		}
		modelled := modelledTags(t, reflect.TypeOf(goldenSamples()[name]))
		declared := map[string]string{}
		for fld, why := range declaredAbsent[name] {
			declared[fld] = why
			// A field the payload tables mark M is never a disposition. Allowing one
			// here would let a mandatory omission be silenced by the same edit that
			// records an inapplicable field, which is the hole this split closes.
			if strings.HasPrefix(why, "M,") {
				t.Errorf("%s/%s is marked M by its payload table but is listed in declaredAbsent. "+
					"A mandatory field is either modelled or a known defect — move it to "+
					"knownConditionalDefects, or model it", name, fld)
			}
		}
		// A defect is separately tracked, because the two lists are stale for different
		// reasons. declaredAbsent says "this package does not model the field, and need
		// not" — modelling it makes that declaration false. knownConditionalDefects says
		// the project meets the condition "and does not populate" it, which is a statement
		// about the emitter, not about this package: a field can be modelled and still not
		// reported, so the staleness rule that applies to the first does not apply to the
		// second.
		defects := map[string]bool{}
		for fld, why := range knownConditionalDefects[name] {
			declared[fld] = why
			defects[fld] = true
		}

		for _, f := range fields {
			_, isModelled := modelled[f.tag]
			_, isDeclared := declared[f.name]
			switch {
			case isModelled && isDeclared && !defects[f.name]:
				staleDeclarations = append(staleDeclarations, fmt.Sprintf(
					"%s/%s is declared absent but is now modelled — remove the declaration", name, f.name))
			case !isModelled && !isDeclared:
				undeclared = append(undeclared, fmt.Sprintf("%s/%s [%d]", name, f.name, f.tag))
			}
		}

		// A declaration naming a field the module no longer defines is also rot.
		for declaredName := range declared {
			found := false
			for _, f := range fields {
				if f.name == declaredName {
					found = true
					break
				}
			}
			if !found {
				staleDeclarations = append(staleDeclarations, fmt.Sprintf(
					"%s/%s is declared absent but the module does not define it", name, declaredName))
			}
		}
	}

	sort.Strings(undeclared)
	sort.Strings(staleDeclarations)

	for _, s := range staleDeclarations {
		t.Errorf("stale declaration: %s", s)
	}
	if len(undeclared) > 0 {
		t.Errorf("%d field(s) the published module defines that this package neither models "+
			"nor declares absent. Judge each against its payload table in TS 33.128 clause 6.2 "+
			"— the module marks nearly everything OPTIONAL, so this list says nothing about which "+
			"are mandatory — then record the verdict in declaredAbsent, or in "+
			"knownConditionalDefects if this project meets the condition and does not report it:\n  %s",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}
}

// TestASN1DriftAuditDetectsAnOmission proves the audit above can fail. Without
// this, a parser that silently produced no fields would report every record as
// clean and read exactly like a passing conformance check.
func TestASN1DriftAuditDetectsAnOmission(t *testing.T) {
	module := moduleSequences(t)

	fields, ok := module["SMFPDUSessionModification"]
	if !ok {
		t.Fatal("SMFPDUSessionModification is not defined in the vendored module")
	}
	byName := map[string]int{}
	for _, f := range fields {
		byName[f.name] = f.tag
	}

	modelled := modelledTags(t, reflect.TypeOf(SMFPDUSessionModification{}))

	// gTPTunnelInfo is marked M in payload table 6.2.3-2 and OPTIONAL in the
	// module, so no decoder catches its absence. This asserts it stays modelled: for
	// this record it is the *only* mandatory field, so losing it would leave the
	// record satisfying none of its mandatory set while still decoding cleanly.
	tag, ok := byName["gTPTunnelInfo"]
	if !ok {
		t.Fatal("the module no longer defines SMFPDUSessionModification/gTPTunnelInfo")
	}
	if _, present := modelled[tag]; !present {
		t.Error("SMFPDUSessionModification no longer models gTPTunnelInfo, which table 6.2.3-2 marks mandatory")
	}

	// The sentinel: a field that is unmodelled, standing in as proof that the audit
	// can see an omission at all. If it becomes modelled, repoint it — a sentinel with
	// nothing left to detect is worse than none.
	//
	// ePSPDNConnectionModification is a deliberately stable choice. It reports an
	// EPS/5GS interworking case this project does not implement, so unlike a deferral
	// it is not waiting on anything and will not quietly become modelled.
	tag, ok = byName["ePSPDNConnectionModification"]
	if !ok {
		t.Fatal("the module no longer defines SMFPDUSessionModification/ePSPDNConnectionModification")
	}
	if _, present := modelled[tag]; present {
		t.Error("ePSPDNConnectionModification is now modelled: repoint this sentinel at a field " +
			"that is not, and check whether the N/A disposition still holds")
	}
	if _, declared := declaredAbsent["SMFPDUSessionModification"]["ePSPDNConnectionModification"]; !declared {
		t.Error("the sentinel field is not declared absent, so the audit above would already be " +
			"failing on it — this test and that one disagree")
	}

	// sUPIUnauthenticated is modelled and the SMF populates it, so it belongs in neither
	// list. It is a pointer member because false is the field's ordinary value, and an
	// OPTIONAL member equal to its type's zero would otherwise be omitted.
	tag, ok = byName["sUPIUnauthenticated"]
	if !ok {
		t.Fatal("the module no longer defines SMFPDUSessionModification/sUPIUnauthenticated")
	}
	if _, present := modelled[tag]; !present {
		t.Error("SMFPDUSessionModification no longer models sUPIUnauthenticated")
	}
	if _, known := knownConditionalDefects["SMFPDUSessionModification"]["sUPIUnauthenticated"]; known {
		t.Error("sUPIUnauthenticated is back in the defect list. It is modelled and the SMF " +
			"populates it from the UnauthenticatedSupi the AMF sends on N11, so a re-entry here " +
			"means one of those two regressed")
	}
	if _, misfiled := declaredAbsent["SMFPDUSessionModification"]["sUPIUnauthenticated"]; misfiled {
		t.Error("sUPIUnauthenticated is filed as a disposition; its condition is one this project " +
			"meets, so it would be a defect, not something declared absent")
	}
}

// TestNoKnownConditionalDefects requires knownConditionalDefects to be empty. Each entry would
// be a field TS 33.128 says these packages should report and do not — a record that still
// decodes cleanly against the published module while carrying less than its table requires,
// so nothing downstream would notice. An entry is a deliberate act: it must be written up in
// CONFORMANCE.md.
func TestNoKnownConditionalDefects(t *testing.T) {
	for record, fields := range knownConditionalDefects {
		for field, why := range fields {
			t.Errorf("%s/%s is listed as a known conditional-field defect (%s); fix it, or write it "+
				"up in CONFORMANCE.md and change this test with the reason", record, field, why)
		}
	}
}

// TestTheDispositionCountsAreTheDocumentedOnes holds the counts CONFORMANCE.md publishes to the
// disposition list they summarise: the markers and the verdicts of every recorded field. A field
// added, removed or re-judged changes a number a reader relies on, so the document has to change
// with it.
func TestTheDispositionCountsAreTheDocumentedOnes(t *testing.T) {
	entry := regexp.MustCompile(`^([CMO]), table [^:]*: ([A-Z][A-Z /]*):`)

	markers, verdicts, total := map[string]int{}, map[string]int{}, 0
	for record, fields := range declaredAbsent {
		for field, disposition := range fields {
			m := entry.FindStringSubmatch(disposition)
			if m == nil {
				t.Errorf("%s/%s: disposition %q does not start with a marker, a table and a verdict",
					record, field, disposition)
				continue
			}
			total++
			markers[m[1]]++
			verdicts[m[2]]++
		}
	}

	// The numbers in CONFORMANCE.md, "Every unmodelled field, and its marker".
	if total != 116 {
		t.Errorf("%d recorded fields, CONFORMANCE.md says 116", total)
	}
	for marker, want := range map[string]int{"M": 0, "C": 115, "O": 1} {
		if markers[marker] != want {
			t.Errorf("%d fields marked %s, CONFORMANCE.md says %d", markers[marker], marker, want)
		}
	}
	for verdict, want := range map[string]int{
		"NOT HELD": 69, "N/A": 26, "DEFERRED": 9, "UNTRACED": 9, "DEPRECATED": 3,
		"MET": 0, "BLOCKED": 0,
	} {
		if verdicts[verdict] != want {
			t.Errorf("%d fields judged %s, CONFORMANCE.md says %d", verdicts[verdict], verdict, want)
		}
	}
}

// declaredAbsentNested is declaredAbsent for the structures below a record, and for the
// CHOICE alternatives this package does not model: the module members they do not
// model, each with the reason. The categories are declaredAbsent's; there is no payload
// table for a nested type, so none cites one.
var declaredAbsentNested = map[string]map[string]string{
	"Location": {
		"positioningInfo":        "DEFERRED: li/iri.Location models only locationInfo.currentLocation; the deeper detail is a documented deferral",
		"locationPresenceReport": "DEFERRED: as positioningInfo",
		"fourGPositioningInfo":   "N/A: an EPS location form; this project implements no EPS access",
		"fourGLocationInfo":      "N/A: an EPS location form; this project implements no EPS access",
		"iMSLocation":            "N/A: IMS location; this project implements no IMS element",
	},
	"LocationInfo": {
		"userLocation":      "DEFERRED: li/iri.LocationInfo models only currentLocation; the userLocation subtree is a documented deferral",
		"geoInfo":           "DEFERRED: as userLocation",
		"rATType":           "DEFERRED: as userLocation; the records that report a RAT type carry it at their own rATType member",
		"timeZone":          "DEFERRED: as userLocation",
		"additionalCellIDs": "DEFERRED: as userLocation",
	},
	"GTPTunnelInfo": {
		"ePSGTPTunnels": "N/A: reports PDN connection events at an SGW/PGW, which this project does not implement",
	},
	"FiveGSGTPTunnels": {
		"additionalULNGUUPTunnelInformation": "NOT HELD: the SMF manages a single default NG-U path and holds no additional uplink tunnel",
		"dLRANTunnelInformation":             "NOT HELD: the SMF holds no downlink RAN tunnel and QoS flow description for the path it manages",
	},
	"UserIdentifiers": {
		"ePSSubscriberIDs": "N/A: carries the EPS forms of the identities, for a UE with an EPS presence, which SD-Core's AMF never has",
	},
	// CHOICEs: alternatives, not members.
	"PEI": {
		"mACAddress": "N/A: the MAC-address and EUI-64 PEI forms belong to devices without an IMEI, reaching the core over wireline access this deployment does not provide; the AMF maps only the imei-/imeisv- forms",
		"eUI64":      "N/A: as mACAddress",
	},
	"FiveGSSubscriberID": {
		"sUCI": "UNTRACED: iri.Identifiers builds the list from the SUPI, PEI and GPSI only; whether the SUCI is reportable in the records carrying UserIdentifiers was not traced",
	},
}

// TestASN1NestedTypeDrift is TestASN1RecordDrift for everything below a record: every
// structure a record reaches, and every CHOICE whose alternatives it carries. It checks
// both directions, as the record audit does — a module member this package neither
// models nor declares absent fails, and so does a declared tag or optionality the
// module does not define. The record audit stops at the record's own fields, so without
// this a nested declaration would be authority over nothing but itself.
func TestASN1NestedTypeDrift(t *testing.T) {
	sequences := moduleSequences(t)
	choices := moduleChoices(t)

	records := map[reflect.Type]bool{}
	for _, sample := range goldenSamples() {
		records[reflect.TypeOf(sample)] = true
	}
	arms := map[reflect.Type]bool{}
	for _, typ := range modelledAlternatives["fiveGSSubscriberID"] {
		arms[typ] = true
	}

	// Every struct type reachable from a record, other than the records and the
	// FiveGSSubscriberID arms, which are CHOICEs and are audited as such below.
	nested := map[reflect.Type]bool{}
	var reach func(typ reflect.Type)
	reach = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || nested[typ] {
			return
		}
		if !records[typ] && !arms[typ] {
			nested[typ] = true
		}
		for i := range typ.NumField() {
			reach(typ.Field(i).Type)
		}
	}
	for typ := range records {
		reach(typ)
	}
	for _, alts := range modelledAlternatives {
		for _, typ := range alts {
			reach(typ)
		}
	}
	for _, want := range []string{
		"SUCI", "FTEID", "Location", "LocationInfo", "GTPTunnelInfo", "FiveGSGTPTunnels", "PLMNID",
		"TAI", "SNSSAI", "UserIdentifiers", "FiveGSSubscriberIDs", "FiveGGUTI", "SMFServingNetwork",
		"AMFID", "PDUSessionResourceInformation",
	} {
		found := false
		for typ := range nested {
			found = found || typ.Name() == want
		}
		if !found {
			t.Errorf("%s is not reached from any record, so this audit no longer covers it", want)
		}
	}

	audited := map[string]bool{}
	for typ := range nested {
		name := typ.Name()
		fields, ok := sequences[name]
		if !ok {
			t.Errorf("%s has no SEQUENCE in %s", name, modulePath)
			continue
		}
		audited[name] = true
		byTag := map[int]asn1TypedField{}
		for _, f := range fields {
			byTag[f.tag] = f
		}
		modelled := map[string]bool{}
		for i := range typ.NumField() {
			sf := typ.Field(i)
			d := declaration(sf)
			if !d.hasTag {
				continue
			}
			f, ok := byTag[d.tag]
			if !ok {
				t.Errorf("%s.%s is declared [%d], which %s does not define", name, sf.Name, d.tag, name)
				continue
			}
			modelled[f.name] = true
			if d.optional != f.optional {
				t.Errorf("%s.%s is declared optional=%v; the module's %s [%d] is optional=%v",
					name, sf.Name, d.optional, f.name, f.tag, f.optional)
			}
		}
		for _, f := range fields {
			_, declared := declaredAbsentNested[name][f.name]
			switch {
			case modelled[f.name] && declared:
				t.Errorf("stale declaration: %s/%s is declared absent but is modelled", name, f.name)
			case !modelled[f.name] && !declared:
				t.Errorf("%s/%s [%d] is defined by the module, neither modelled nor declared absent",
					name, f.name, f.tag)
			}
		}
	}

	// The CHOICEs. XIRIEvent is the record audit's, which is scoped to the records this
	// package emits; the module's other events are not this package's to declare.
	for choice, types := range modelledAlternatives {
		if choice == "xiriEvent" {
			continue
		}
		module := choiceModules[choice]
		audited[module] = true
		modelled := map[string]bool{}
		for _, typ := range types {
			for _, alt := range choices[module] {
				if name, named := alternativeNames[typ]; (named && alt.name == name) || (!named && alt.typeName == typ.Name()) {
					modelled[alt.name] = true
				}
			}
		}
		if len(modelled) != len(types) {
			t.Errorf("%s: %d Go alternatives map to %d module alternatives", module, len(types), len(modelled))
		}
		for _, alt := range choices[module] {
			_, declared := declaredAbsentNested[module][alt.name]
			switch {
			case modelled[alt.name] && declared:
				t.Errorf("stale declaration: %s/%s is declared absent but is modelled", module, alt.name)
			case !modelled[alt.name] && !declared:
				t.Errorf("%s/%s [%d] is an alternative the module defines, neither modelled nor declared absent",
					module, alt.name, alt.tag)
			}
		}
	}

	for name, members := range declaredAbsentNested {
		if !audited[name] {
			t.Errorf("declaredAbsentNested names %s, which this audit does not reach", name)
			continue
		}
		for member := range members {
			found := false
			for _, f := range sequences[name] {
				found = found || f.name == member
			}
			for _, alt := range choices[name] {
				found = found || alt.name == member
			}
			if !found {
				t.Errorf("stale declaration: %s/%s is declared absent but the module does not define it", name, member)
			}
		}
	}
}
