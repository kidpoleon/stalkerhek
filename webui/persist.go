package webui

import (
    "encoding/json"
    "os"
)

var profilesFile = "profiles.json"

// SaveProfiles writes current profiles to disk
func SaveProfiles() error {
    profMu.RLock()
    defer profMu.RUnlock()
    f, err := os.Create(profilesFile)
    if err != nil { return err }
    defer f.Close()
    enc := json.NewEncoder(f)
    enc.SetIndent("", "  ")
    return enc.Encode(profiles)
}

// LoadProfiles loads profiles from disk (if exists)
func LoadProfiles() error {
    f, err := os.Open(profilesFile)
    if err != nil { return err }
    defer f.Close()
    var arr []Profile
    if err := json.NewDecoder(f).Decode(&arr); err != nil { return err }
    profMu.Lock()
    profiles = arr
    nextProfile = 1
    for _, p := range profiles { if p.ID >= nextProfile { nextProfile = p.ID + 1 } }
    profMu.Unlock()
    return nil
}
