package oah

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func validateReceiverOptions(c AudioConfig) error {
	switch c.SecondarySBCMaxBitpool {
	case 35, 53, 64, 250:
	default:
		return fmtErr("secondary SBC maximum must be 35, 53, 64 or 250")
	}
	if c.SecondaryAdvertisedDelayMS < 0 || c.SecondaryAdvertisedDelayMS > 2000 {
		return fmtErr("secondary advertised delay must be 0–2000 ms; 0 uses the engine default")
	}
	return nil
}

// The bridge is unprivileged. Never give it the authentication-bearing config.
func (a *App) writeAudioProjection(cfg AudioConfig) error {
	if err := validateReceiverOptions(cfg); err != nil {
		return err
	}
	dir := filepath.Dir(a.cfg.path)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".audio-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0644); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(dir, "audio.json"))
}
