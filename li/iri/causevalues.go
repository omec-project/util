// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

// The permitted values of the five HandoverCause groups, transcribed from
// `testdata/asn1/TS33128Payloads.asn` — the TS 33.128 ASN.1 module this package already holds
// for schema validation.
//
// **They are here because they are TS 33.128's values.** An element that builds a handover
// record reads its cause from another protocol — NGAP, whose Cause groups describe the same
// concepts and number them differently — and the correspondence between the two is that
// element's knowledge, not this package's. What is this package's is what its own definition
// permits, and naming those values is what lets an element map *to* a named value instead of
// to an integer it has to get right by arithmetic.
//
// Every value below is asserted against the module in TestCauseValuesMatchTheModule, so a
// release that renumbers or extends a group fails a test here rather than being discovered
// downstream. They are transcribed by hand; the test is what keeps them honest.

// The CauseRadioNetwork group: 51 values, 1..52.
const (
	CauseRadioNetworkUnspecified                                              CauseRadioNetwork = 1
	CauseRadioNetworkTxnrelocoverallExpiry                                    CauseRadioNetwork = 2
	CauseRadioNetworkSuccessfulHandover                                       CauseRadioNetwork = 3
	CauseRadioNetworkReleaseDueToNGRANGeneratedReason                         CauseRadioNetwork = 4
	CauseRadioNetworkReleaseDueTo5gcGeneratedReason                           CauseRadioNetwork = 5
	CauseRadioNetworkHandoverCancelled                                        CauseRadioNetwork = 6
	CauseRadioNetworkPartialHandover                                          CauseRadioNetwork = 7
	CauseRadioNetworkHoFailureInTarget5GCNGRANNodeOrTargetSystem              CauseRadioNetwork = 8
	CauseRadioNetworkHoTargetNotAllowed                                       CauseRadioNetwork = 9
	CauseRadioNetworkTNGRelocOverallExpiry                                    CauseRadioNetwork = 10
	CauseRadioNetworkTNGRelocPrepExpiry                                       CauseRadioNetwork = 11
	CauseRadioNetworkCellNotAvailable                                         CauseRadioNetwork = 12
	CauseRadioNetworkUnknownTargetID                                          CauseRadioNetwork = 13
	CauseRadioNetworkNoRadioResourcesAvailableInTargetCell                    CauseRadioNetwork = 14
	CauseRadioNetworkUnknownLocalUENGAPID                                     CauseRadioNetwork = 15
	CauseRadioNetworkInconsistentRemoteUENGAPID                               CauseRadioNetwork = 16
	CauseRadioNetworkHandoverDesirableForRadioReason                          CauseRadioNetwork = 17
	CauseRadioNetworkTimeCriticalHandover                                     CauseRadioNetwork = 18
	CauseRadioNetworkResourceOptimisationHandover                             CauseRadioNetwork = 19
	CauseRadioNetworkReduceLoadInServingCell                                  CauseRadioNetwork = 20
	CauseRadioNetworkUserInactivity                                           CauseRadioNetwork = 21
	CauseRadioNetworkRadioConnectionWithUELost                                CauseRadioNetwork = 22
	CauseRadioNetworkRadioResourcesNotAvailable                               CauseRadioNetwork = 23
	CauseRadioNetworkInvalidQoSCombination                                    CauseRadioNetwork = 24
	CauseRadioNetworkFailureInRadioInterfaceProcedure                         CauseRadioNetwork = 25
	CauseRadioNetworkInteractionWithOtherProcedure                            CauseRadioNetwork = 26
	CauseRadioNetworkUnknownPDUSessionID                                      CauseRadioNetwork = 27
	CauseRadioNetworkMultiplePDUSessionIDInstances                            CauseRadioNetwork = 29
	CauseRadioNetworkMultipleQoSFlowIDInstances                               CauseRadioNetwork = 30
	CauseRadioNetworkEncryptionAndOrIntegrityProtectionAlgorithmsNotSupported CauseRadioNetwork = 31
	CauseRadioNetworkNGIntraSystemHandoverTriggered                           CauseRadioNetwork = 32
	CauseRadioNetworkNGInterSystemHandoverTriggered                           CauseRadioNetwork = 33
	CauseRadioNetworkXNHandoverTriggered                                      CauseRadioNetwork = 34
	CauseRadioNetworkNotSupported5QIValue                                     CauseRadioNetwork = 35
	CauseRadioNetworkUEContextTransfer                                        CauseRadioNetwork = 36
	CauseRadioNetworkIMSVoiceeEPSFallbackOrRATFallbackTriggered               CauseRadioNetwork = 37
	CauseRadioNetworkUPIntegrityProtectioNotPossible                          CauseRadioNetwork = 38
	CauseRadioNetworkUPConfidentialityProtectionNotPossible                   CauseRadioNetwork = 39
	CauseRadioNetworkSliceNotSupported                                        CauseRadioNetwork = 40
	CauseRadioNetworkUEInRRCInactiveStateNotReachable                         CauseRadioNetwork = 41
	CauseRadioNetworkRedirection                                              CauseRadioNetwork = 42
	CauseRadioNetworkResourcesNotAvailableForTheSlice                         CauseRadioNetwork = 43
	CauseRadioNetworkUEMaxIntegrityProtectedDataRateReason                    CauseRadioNetwork = 44
	CauseRadioNetworkReleaseDueToCNDetectedMobility                           CauseRadioNetwork = 45
	CauseRadioNetworkN26InterfaceNotAvailable                                 CauseRadioNetwork = 46
	CauseRadioNetworkReleaseDueToPreemption                                   CauseRadioNetwork = 47
	CauseRadioNetworkMultipleLocationReportingReferenceIDInstances            CauseRadioNetwork = 48
	CauseRadioNetworkRSNNotAvailableForTheUP                                  CauseRadioNetwork = 49
	CauseRadioNetworkNPMAccessDenied                                          CauseRadioNetwork = 50
	CauseRadioNetworkCAGOnlyAccessDenied                                      CauseRadioNetwork = 51
	CauseRadioNetworkInsufficientUECapabilities                               CauseRadioNetwork = 52
)

// The CauseTransport group: 2 values, 1..2.
const (
	CauseTransportTransportResourceUnavailable CauseTransport = 1
	CauseTransportUnspecified                  CauseTransport = 2
)

// The CauseNas group: 4 values, 1..4.
const (
	CauseNasNormalRelease         CauseNas = 1
	CauseNasAuthenticationFailure CauseNas = 2
	CauseNasDeregister            CauseNas = 3
	CauseNasUnspecified           CauseNas = 4
)

// The CauseProtocol group: 7 values, 1..7.
const (
	CauseProtocolTransferSyntaxError                          CauseProtocol = 1
	CauseProtocolAbstractSyntaxErrorReject                    CauseProtocol = 2
	CauseProtocolAbstractSyntaxErrorIgnoreAndNotify           CauseProtocol = 3
	CauseProtocolMessageNotCompatibleWithReceiverState        CauseProtocol = 4
	CauseProtocolSemanticError                                CauseProtocol = 5
	CauseProtocolAbstractSyntaxErrorFalselyConstructedMessage CauseProtocol = 6
	CauseProtocolUnspecified                                  CauseProtocol = 7
)

// The CauseMisc group: 6 values, 1..6.
const (
	CauseMiscControlProcessingOverload             CauseMisc = 1
	CauseMiscNotEnoughUserPlaneProcessingResources CauseMisc = 2
	CauseMiscHardwareFailure                       CauseMisc = 3
	CauseMiscOMIntervention                        CauseMisc = 4
	CauseMiscUnknownPLMNOrSNPN                     CauseMisc = 5
	CauseMiscUnspecified                           CauseMisc = 6
)
