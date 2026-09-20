package oah

import "time"

type Config struct {
	Revision      uint64                 `json:"revision"`
	Listen        string                 `json:"listen"`
	AudioUser     string                 `json:"audioUser"`
	AudioUID      int                    `json:"audioUid"`
	PasswordSalt  string                 `json:"passwordSalt"`
	PasswordHash  string                 `json:"passwordHash"`
	BluetoothName string                 `json:"bluetoothName"`
	Slots         SlotsConfig            `json:"slots"`
	Mixer         MixerConfig            `json:"mixer"`
	Audio         AudioConfig            `json:"audio"`
	UI            UIConfig               `json:"ui"`
	WiFi          WiFiConfig             `json:"wifi"`
	DevicePrefs   map[string]DevicePrefs `json:"devicePrefs,omitempty"`
}

type DevicePrefs struct {
	AutoConnect bool `json:"autoConnect"`
}

type SlotsConfig struct {
	Inputs  []string `json:"inputs"`
	Outputs []string `json:"outputs"`
}

type MixerConfig struct {
	Gains      []float64 `json:"gains"`
	Mutes      []bool    `json:"mutes"`
	Placement  []string  `json:"placement"`
	Master     float64   `json:"master"`
	MasterMute bool      `json:"masterMute"`
	HeadroomDB float64   `json:"headroomDb"`
	Limiter    bool      `json:"limiter"`
}

type AudioConfig struct {
	SecondarySBCMaxBitpool     int    `json:"secondarySbcMaxBitpool"`
	SecondaryAdvertisedDelayMS int    `json:"secondaryAdvertisedDelayMs"`
	Preset                     string `json:"preset"`
	PreferredRate              int    `json:"preferredRate"`
	AllowedRates               []int  `json:"allowedRates"`
	Quantum                    int    `json:"quantum"`
	BlueALSAPeriodUS           int    `json:"bluealsaPeriodUs"`
	BlueALSABufferUS           int    `json:"bluealsaBufferUs"`
	Resampler                  string `json:"resampler"`
	CodecPolicy                string `json:"codecPolicy"`
	LiveMeters                 bool   `json:"liveMeters"`
}

type UIConfig struct {
	PairingTimeoutSec int `json:"pairingTimeoutSec"`
}

type WiFiConfig struct {
	Interface     string `json:"interface"`
	PreferredBand string `json:"preferredBand"`
}

type Device struct {
	SBCMaxBitpool int      `json:"sbcMaxBitpool,omitempty"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Addr          string   `json:"addr"`
	Icon          string   `json:"icon,omitempty"`
	Kind          string   `json:"kind"`
	Caps          []string `json:"caps"`
	Paired        bool     `json:"paired"`
	Trusted       bool     `json:"trusted"`
	AutoConnect   bool     `json:"autoConnect"`
	Connected     bool     `json:"connected"`
	Status        string   `json:"status"`
	RSSI          int      `json:"rssi,omitempty"`
	Codec         string   `json:"codec,omitempty"`
	Rate          int      `json:"rate,omitempty"`
	Role          string   `json:"role,omitempty"`
	Volume        int      `json:"volume,omitempty"`
	VolumeKnown   bool     `json:"volumeKnown,omitempty"`
	Muted         bool     `json:"muted,omitempty"`
	LatencyMS     int      `json:"latencyMs,omitempty"`
	Backend       string   `json:"backend,omitempty"`
	// Reason explains a status the user cannot act on otherwise, such as another
	// output already holding the single A2DP source endpoint.
	Reason string `json:"reason,omitempty"`
}

type WiFiNetwork struct {
	SSID    string `json:"ssid"`
	BSSID   string `json:"bssid,omitempty"`
	Freq    int    `json:"freq"`
	Band    string `json:"band"`
	Channel int    `json:"channel"`
	RSSI    int    `json:"rssi"`
	Secure  bool   `json:"secure"`
}

type WiFiState struct {
	Interface string        `json:"interface"`
	SSID      string        `json:"ssid"`
	BSSID     string        `json:"bssid"`
	Freq      int           `json:"freq"`
	Band      string        `json:"band"`
	Channel   int           `json:"channel"`
	RSSI      int           `json:"rssi"`
	IP        string        `json:"ip"`
	Networks  []WiFiNetwork `json:"networks"`
	Applying  *NetworkApply `json:"apply,omitempty"`
}

type NetworkApply struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	SSID      string    `json:"ssid"`
	StartedAt time.Time `json:"startedAt"`
	Message   string    `json:"message,omitempty"`
}

type Health struct {
	WiFi struct {
		State   string `json:"state"`
		Value   string `json:"value"`
		Warning bool   `json:"warning"`
	} `json:"wifi"`
	Bluetooth struct {
		State         string `json:"state"`
		Value         string `json:"value"`
		ActiveSources int    `json:"activeSources"`
		ActiveOutputs int    `json:"activeOutputs"`
	} `json:"bluetooth"`
	Audio struct {
		State string `json:"state"`
		Value string `json:"value"`
		Xruns int    `json:"xruns"`
	} `json:"audio"`
	System struct {
		State string `json:"state"`
		Value string `json:"value"`
	} `json:"system"`
}

type Diagnostics struct {
	CPUPercent float64       `json:"cpuPercent"`
	MemPercent float64       `json:"memPercent"`
	TempC      float64       `json:"tempC"`
	Xruns      int           `json:"xruns"`
	Services   []ServiceInfo `json:"services"`
	Transports []Transport   `json:"transports"`
	Logs       []string      `json:"logs"`
}

type ServiceInfo struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type Transport struct {
	SBCMaxBitpool int    `json:"sbcMaxBitpool,omitempty"`
	Path          string `json:"path"`
	Addr          string `json:"addr"`
	UUID          string `json:"uuid"`
	Codec         string `json:"codec"`
	Rate          int    `json:"rate"`
	State         string `json:"state"`
	Volume        int    `json:"volume"`
	VolumeKnown   bool   `json:"volumeKnown,omitempty"`
	Delay         int    `json:"delay"`
	// DelayKnown distinguishes "BlueZ reports a delay of 0" from "BlueZ exposes
	// no delay property for this transport". Without it an absent property reads
	// as a confirmed zero and the delay report would claim success. Units are
	// 0.1 ms, as defined by BlueZ MediaTransport1.
	DelayKnown bool `json:"delayKnown,omitempty"`
}

type SystemState struct {
	Hostname string `json:"hostname"`
	MDNS     string `json:"mdns"`
	BTName   string `json:"btName"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Time     string `json:"time"`
	Uptime   string `json:"uptime"`
}

type PairingState struct {
	Active   bool      `json:"active"`
	Until    time.Time `json:"until,omitempty"`
	Scanning bool      `json:"scanning"`
}

type State struct {
	Revision    uint64       `json:"revision"`
	Devices     []Device     `json:"devices"`
	Slots       SlotsConfig  `json:"slots"`
	Mixer       MixerConfig  `json:"mixer"`
	WiFi        WiFiState    `json:"wifi"`
	Audio       AudioConfig  `json:"audio"`
	DelayReport DelayReport  `json:"delayReport"`
	System      SystemState  `json:"system"`
	Health      Health       `json:"health"`
	Diagnostics Diagnostics  `json:"diagnostics"`
	Pairing     PairingState `json:"pairing"`
}
