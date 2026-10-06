// SPDX-FileCopyrightText: 2022-present Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package metricinfo

type CoreSubscriber struct {
	UpfName     string `json:"upfid,omitempty"`
	UpfAddr     string `json:"upfAddr,omitempty"`
	SmfId       string `json:"smfId,omitempty"`
	SmfIp       string `json:"smfIp,omitempty"`
	SmfSubState string `json:"smfSubState,omitempty"`
	IPAddress   string `json:"ipaddress,omitempty"`
	Dnn         string `json:"dnn,omitempty"`
	Slice       string `json:"slice,omitempty"`
	UeState     string `json:"ueState,omitempty"`
	AmfId       string `json:"amfId,omitempty"`
	Imsi        string `json:"imsi,omitempty"`
	AmfIp       string `json:"amfIp,omitempty"`
	TacId       string `json:"tacid,omitempty"`
	Guti        string `json:"guti,omitempty"`
	GnbId       string `json:"gnbid,omitempty"`
	AmfSubState string `json:"amfSubState,omitempty"`
	RanNgapId   int64  `json:"ranngapId,omitempty"`
	AmfNgapId   int64  `json:"amfngapId,omitempty"`
	RSEID       int    `json:"rseid,omitempty"`
	Version     int    `json:"version,omitempty"`
	LSEID       int    `json:"lseid,omitempty"`
	Tmsi        int32  `json:"tmsi,omitempty"`
}

type CoreEventType int64

const (
	CSubscriberEvt CoreEventType = iota
	CMsgTypeEvt
	CNfStatusEvt
)

func (e CoreEventType) String() string {
	switch e {
	case CSubscriberEvt:
		return "SubscriberEvt"
	case CMsgTypeEvt:
		return "MsgTypeEvt"
	case CNfStatusEvt:
		return "CNfStatusEvt"
	}
	return "Unknown"
}

type NfStatusType string

const (
	NfStatusConnected    NfStatusType = "Connected"
	NfStatusDisconnected NfStatusType = "Disconnected"
)

type NfType string

const (
	NfTypeSmf NfType = "SMF"
	NfTypeAmf NfType = "AMF"
	NfTypeUPF NfType = "UPF"
	NfTypeGnb NfType = "GNB"
	NfTypeEnd NfType = "Invalid"
)

type CNfStatus struct {
	NfType   NfType       `json:"nfType,omitempty"`
	NfStatus NfStatusType `json:"nfStatus,omitempty"`
	NfName   string       `json:"nfName,omitempty"`
}

type SubscriberOp uint

const (
	SubsOpAdd SubscriberOp = iota + 1
	SubsOpMod
	SubsOpDel
)

type CoreSubscriberData struct {
	Subscriber CoreSubscriber `json:"subscriber,omitempty"`
	Operation  SubscriberOp   `json:"subsOp,omitempty"`
}

// Sent by NFs(Producers) and received by Metric Function
type MetricEvent struct {
	NfStatusData   CNfStatus          `json:"nfStatusData"`
	SubscriberData CoreSubscriberData `json:"subscriberData,omitempty"`
	EventType      CoreEventType      `json:"eventType,omitempty"`
}
