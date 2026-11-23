package types

import "encoding/json"

// Flex handles JSON fields that can be a single object, an array, or null.
type Flex[T any] struct {
	Items []T
}

func (f *Flex[T]) UnmarshalJSON(data []byte) error {
	trimmed := json.Valid(data)
	if !trimmed {
		f.Items = nil
		return nil
	}

	// Try array
	var arr []T
	if err := json.Unmarshal(data, &arr); err == nil {
		f.Items = arr
		return nil
	}

	// Try single object
	var single T
	if err := json.Unmarshal(data, &single); err == nil {
		f.Items = []T{single}
		return nil
	}

	// null
	f.Items = nil
	return nil
}

// ---------------------------------------------------------------------------
// Top-level
// ---------------------------------------------------------------------------

type NmapRun struct {
	Scanner   string     `json:"Scanner"`
	Args      string     `json:"Args"`
	Start     int64      `json:"Start"`
	StartStr  string     `json:"StartStr"`
	Version   string     `json:"Version"`
	ScanInfo  ScanInfo   `json:"ScanInfo"`
	Host      Flex[Host] `json:"Host"`
	Verbose   Verbose    `json:"Verbose"`
	Debugging Verbose    `json:"Debugging"`
	RunStats  RunStats   `json:"RunStats"`
}

type ScanInfo struct {
	Type        string `json:"Type"`
	Protocol    string `json:"Protocol"`
	NumServices int    `json:"NumServices"`
	Services    string `json:"Services"`
}

type Verbose struct {
	Level int `json:"Level"`
}

type RunStats struct {
	Finished Finished `json:"Finished"`
	Hosts    Hosts    `json:"Hosts"`
}

type Finished struct {
	Time    int64   `json:"Time"`
	TimeStr string  `json:"TimeStr"`
	Elapsed float64 `json:"Elapsed"`
	Summary string  `json:"Summary"`
	Exit    string  `json:"Exit"`
}

type Hosts struct {
	Up    int `json:"Up"`
	Down  int `json:"Down"`
	Total int `json:"Total"`
}

// ---------------------------------------------------------------------------
// Host
// ---------------------------------------------------------------------------

type Host struct {
	StartTime     int64             `json:"StartTime"`
	EndTime       int64             `json:"EndTime"`
	Port          Flex[Port]        `json:"Port"`
	HostAddress   Flex[HostAddress] `json:"HostAddress"`
	HostNames     HostNames         `json:"HostNames"`
	Status        HostStatus        `json:"Status"`
	OS            OS                `json:"OS"`
	Trace         Trace             `json:"Trace"`
	Uptime        Uptime            `json:"Uptime"`
	Distance      Distance          `json:"Distance"`
	TCPSequence   TCPSequence       `json:"TCPSequence"`
	IPIDSequence  IPIDSequence      `json:"IPIDSequence"`
	TCPTSSequence TCPTSSequence     `json:"TCPTSSequence"`
}

type HostNames struct {
	HostName Flex[HostName] `json:"HostName"`
}

type HostName struct {
	Name string `json:"Name"`
	Type string `json:"Type"`
}

type HostStatus struct {
	State  string `json:"State"`
	Reason string `json:"Reason"`
}

// ---------------------------------------------------------------------------
// Port
// ---------------------------------------------------------------------------

type Port struct {
	Protocol string       `json:"Protocol"`
	PortID   int          `json:"PortID"`
	State    PortState    `json:"State"`
	Service  Service      `json:"Service"`
	Script   Flex[Script] `json:"Script"`
}

type PortState struct {
	State     string `json:"State"`
	Reason    string `json:"Reason"`
	ReasonTTL string `json:"ReasonTTL"`
}

type Service struct {
	Name      string   `json:"Name"`
	Product   string   `json:"Product"`
	Version   string   `json:"Version"`
	ExtraInfo string   `json:"ExtraInfo"`
	Method    string   `json:"Method"`
	Conf      string   `json:"Conf"`
	CPE       []string `json:"CPE"` // null-safe: stays nil
}

// Script can be a single object or array of objects (NSE output)
type Script struct {
	ID       string            `json:"ID"`
	Output   string            `json:"Output"`
	Elements Flex[ScriptElem]  `json:"Elements"`
	Tables   Flex[ScriptTable] `json:"Tables"`
}

type ScriptElem struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type ScriptTable struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// ---------------------------------------------------------------------------
// Address
// ---------------------------------------------------------------------------

type HostAddress struct {
	Address     string `json:"Address"`
	AddressType string `json:"AddressType"`
	Vendor      string `json:"Vendor"`
}

// ---------------------------------------------------------------------------
// OS
// ---------------------------------------------------------------------------

type OS struct {
	OSPortUsed Flex[OSPortUsed] `json:"OSPortUsed"`
	OSClass    Flex[OSClass]    `json:"OSClass"`
	OSMatch    Flex[OSMatch]    `json:"OSMatch"`
}

type OSPortUsed struct {
	State  string `json:"State"`
	Proto  string `json:"Proto"`
	PortID int    `json:"PortID"`
}

type OSClass struct {
	Type     string   `json:"Type"`
	Vendor   string   `json:"Vendor"`
	OSFamily string   `json:"OSFamily"`
	CPE      []string `json:"CPE"`
	Accuracy float64  `json:"Accuracy"`
}

type OSMatch struct {
	Name     string  `json:"Name"`
	Accuracy float64 `json:"Accuracy"`
}

// ---------------------------------------------------------------------------
// Trace
// ---------------------------------------------------------------------------

type Trace struct {
	Port     int            `json:"Port"`
	Protocol string         `json:"Protocol"`
	Hops     Flex[TraceHop] `json:"Hops"`
}

type TraceHop struct {
	IP   string  `json:"IP"`
	Host string  `json:"Host"`
	RTT  float64 `json:"RTT"`
	TTL  int     `json:"TTL"`
}

// ---------------------------------------------------------------------------
// Misc
// ---------------------------------------------------------------------------

type Uptime struct {
	Seconds  int64  `json:"Seconds"`
	LastBoot string `json:"LastBoot"`
}

type Distance struct {
	Value int `json:"Value"`
}

type TCPSequence struct {
	Index      string `json:"Index"`
	Difficulty string `json:"Difficulty"`
	Values     string `json:"Values"`
}

type IPIDSequence struct {
	Class  string `json:"Class"`
	Values string `json:"Values"`
}

type TCPTSSequence struct {
	Class  string `json:"Class"`
	Values string `json:"Values"`
}
