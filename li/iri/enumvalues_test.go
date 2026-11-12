// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import "testing"

// TestNamedEnumeratorsMatchTheModule holds every named enumeration constant outside the cause
// groups (which TestCauseValuesMatchTheModule covers) to the value the module gives the same
// enumerator. The bounds tests catch a value outside a type's range; this catches a constant
// that is in range and names the wrong enumerator — a registration reported as periodic when it
// was mobility, which every receiver would accept.
func TestNamedEnumeratorsMatchTheModule(t *testing.T) {
	type constant struct {
		typ, enumerator string
		value           int64
	}
	constants := []constant{
		{"AMFRegistrationType", "initial", int64(RegTypeInitial)},
		{"AMFRegistrationType", "mobility", int64(RegTypeMobility)},
		{"AMFRegistrationType", "periodic", int64(RegTypePeriodic)},
		{"AMFRegistrationType", "emergency", int64(RegTypeEmergency)},
		{"AMFRegistrationType", "sNPNOnboarding", int64(RegTypeSNPNOnboarding)},
		{"AMFRegistrationType", "disasterMobility", int64(RegTypeDisasterMobility)},
		{"AMFRegistrationType", "disasterInitial", int64(RegTypeDisasterInitial)},
		{"AMFRegistrationResult", "threeGPPAccess", int64(RegResult3GPPAccess)},
		{"AMFRegistrationResult", "nonThreeGPPAccess", int64(RegResultNon3GPPAccess)},
		{"AMFRegistrationResult", "threeGPPAndNonThreeGPPAccess", int64(RegResult3GPPAndNon3GPP)},
		{"AMFDirection", "networkInitiated", int64(DirNetworkInitiated)},
		{"AMFDirection", "uEInitiated", int64(DirUEInitiated)},
		{"AccessType", "threeGPPAccess", int64(AccessThreeGPP)},
		{"AccessType", "nonThreeGPPAccess", int64(AccessNonThreeGPP)},
		{"AccessType", "threeGPPandNonThreeGPPAccess", int64(AccessBoth)},
		{"PDUSessionType", "iPv4", int64(PDUSessionTypeIPv4)},
		{"PDUSessionType", "iPv6", int64(PDUSessionTypeIPv6)},
		{"PDUSessionType", "iPv4v6", int64(PDUSessionTypeIPv4v6)},
		{"PDUSessionType", "unstructured", int64(PDUSessionTypeUnstructured)},
		{"PDUSessionType", "ethernet", int64(PDUSessionTypeEthernet)},
		{"FiveGSMRequestType", "initialRequest", int64(SMRequestInitial)},
		{"FiveGSMRequestType", "existingPDUSession", int64(SMRequestExisting)},
		{"FiveGSMRequestType", "initialEmergencyRequest", int64(SMRequestInitialEmergency)},
		{"FiveGSMRequestType", "existingEmergencyPDUSession", int64(SMRequestExistingEmergency)},
		{"FiveGSMRequestType", "modificationRequest", int64(SMRequestModification)},
		{"FiveGSMRequestType", "reserved", int64(SMRequestReserved)},
		{"FiveGSMRequestType", "mAPDURequest", int64(SMRequestMAPDU)},
		{"AMFFailedProcedureType", "registration", int64(FailedRegistration)},
		{"AMFFailedProcedureType", "sMS", int64(FailedSMS)},
		{"AMFFailedProcedureType", "pDUSessionEstablishment", int64(FailedPDUSessionEstablishment)},
		{"SMFFailedProcedureType", "pDUSessionEstablishment", int64(SMFFailedPDUSessionEstablishment)},
		{"SMFFailedProcedureType", "pDUSessionModification", int64(SMFFailedPDUSessionModification)},
		{"SMFFailedProcedureType", "pDUSessionRelease", int64(SMFFailedPDUSessionRelease)},
		{"Initiator", "uE", int64(InitiatorUE)},
		{"Initiator", "network", int64(InitiatorNetwork)},
		{"Initiator", "unknown", int64(InitiatorUnknown)},
		{"HandoverType", "intra5GS", int64(HandoverIntra5GS)},
		{"HandoverType", "fiveGStoEPS", int64(HandoverFiveGSToEPS)},
		{"HandoverType", "ePSto5GS", int64(HandoverEPSTo5GS)},
		{"HandoverType", "fiveGStoUTRA", int64(HandoverFiveGSToUTRA)},
		{"RATType", "nR", int64(RATNR)},
		{"RATType", "eUTRA", int64(RATEUTRA)},
		{"RATType", "wLAN", int64(RATWLAN)},
		{"RATType", "virtual", int64(RATVirtual)},
		{"RATType", "nBIOT", int64(RATNBIOT)},
		{"RATType", "lTEM", int64(RATLTEM)},
		{"RATType", "nRU", int64(RATNRU)},
		{"RATType", "eUTRAU", int64(RATEUTRAU)},
		{"RATType", "trustedN3GA", int64(RATTrustedN3GA)},
		{"RATType", "trustedWLAN", int64(RATTrustedWLAN)},
		{"RATType", "nRLEO", int64(RATNRLEO)},
		{"RATType", "nRMEO", int64(RATNRMEO)},
		{"RATType", "nRGEO", int64(RATNRGEO)},
		{"RATType", "nROTHERSAT", int64(RATNROtherSat)},
		{"RATType", "nRREDCAP", int64(RATNRRedCap)},
	}

	types := map[string]bool{}
	for _, c := range constants {
		types[c.typ] = true
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	module := moduleEnumerators(t, names...)

	for _, c := range constants {
		want, ok := module[c.typ][c.enumerator]
		if !ok {
			t.Errorf("%s has no enumerator %s in the module", c.typ, c.enumerator)
			continue
		}
		if c.value != want {
			t.Errorf("the constant for %s.%s is %d; the module gives %d", c.typ, c.enumerator, c.value, want)
		}
	}
}
