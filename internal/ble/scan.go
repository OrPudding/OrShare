package ble

import (
    "context"
    "encoding/hex"
    "fmt"
    "strings"
    "sync"

    "github.com/godbus/dbus/v5"
)

// DiscoveredDevice represents a device discovered via BLE advertisement.  It
// contains the minimal information encoded in the CatShare advertising
// protocol: a sender identifier (ID), the human‑readable device name,
// optional brand, and whether the peer indicates 5GHz Wi‑Fi capability.
type DiscoveredDevice struct {
    ID           string
    Name         string
    Brand        string
    Supports5Ghz bool
}

// brandNameById maps the brand ID byte into a human‑readable brand name.  It
// follows the same ranges defined in CatShare's DeviceUtils.deviceNameById().
// Unknown IDs return an empty string.
func brandNameById(id byte) string {
    switch {
    case id >= 10 && id <= 19:
        if id == 11 {
            return "realme"
        }
        return "OPPO"
    case id >= 20 && id <= 29:
        return "vivo"
    case id >= 30 && id <= 39:
        return "Xiaomi"
    case id >= 41 && id <= 45:
        return "OnePlus"
    case id >= 50 && id <= 59:
        return "Meizu"
    case id >= 70 && id <= 75:
        return "Samsung"
    case id >= 100 && id <= 109:
        return "Lenovo"
    default:
        return ""
    }
}

// parseServiceData interprets a service data entry from a BLE advertisement.
// It expects data lengths of 6 or 27 bytes according to the CatShare
// specification.  The returned ID and name may be empty if the data does not
// match expectations.  The flag and brand values are taken from the UUID
// rather than the data; see StartAdvertising() for details.
func parseServiceData(uuidStr string, data []byte) (id string, name string, ok bool) {
    if len(data) == 27 {
        // bytes 8–9 of the data contain the senderId (big‑endian)
        sid := uint16(data[8])<<8 | uint16(data[9])
        id = fmt.Sprintf("%04x", sid)
        // bytes 10..25 contain UTF‑8 device name truncated to 15 bytes
        nameBytes := make([]byte, 0, 16)
        for i := 10; i < 26; i++ {
            b := data[i]
            if b == 0 {
                break
            }
            nameBytes = append(nameBytes, b)
        }
        n := string(nameBytes)
        // If truncated, a tab character indicates continuation; replace with …
        if strings.HasSuffix(n, "\t") {
            n = strings.TrimSuffix(n, "\t") + "…"
        }
        name = n
        ok = true
    } else {
        ok = false
    }
    return
}

// parseUUIDFlags extracts flags from the UUID string of a service data entry.
// The CatShare protocol encodes the 5GHz support flag in byte 2 and the
// brand identifier in byte 3 of the UUID.  The UUID string is in the usual
// 8-4-4-4-12 hex format.  If parsing fails the returned values are false
// and 0.
func parseUUIDFlags(uuidStr string) (supports5 bool, brand byte) {
    // Remove hyphens
    hexStr := strings.ReplaceAll(uuidStr, "-", "")
    // Need exactly 32 hex chars
    if len(hexStr) != 32 {
        return false, 0
    }
    bytes, err := hex.DecodeString(hexStr)
    if err != nil || len(bytes) != 16 {
        return false, 0
    }
    supports5 = bytes[2] == 1
    brand = bytes[3]
    return supports5, brand
}

// StartScanning starts BLE scanning on the default adapter (hci0) and
// invokes the callback for each discovered device that advertises the
// CatShare service UUID.  Scanning continues until the context is
// cancelled.  The callback may be invoked multiple times if a device's
// properties change.
func StartScanning(ctx context.Context, callback func(DiscoveredDevice)) error {
    conn, err := dbus.SystemBus()
    if err != nil {
        return fmt.Errorf("failed to connect to system bus: %w", err)
    }
    adapterPath := dbus.ObjectPath("/org/bluez/hci0")
    adapter := conn.Object("org.bluez", adapterPath)
    // Restrict scanning to LE only and filter by our service UUID.
    filter := map[string]dbus.Variant{
        "Transport": dbus.MakeVariant("le"),
        "UUIDs":     dbus.MakeVariant([]string{advServiceUUID}),
    }
    if call := adapter.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0, filter); call.Err != nil {
        return fmt.Errorf("failed to set discovery filter: %w", call.Err)
    }
    if call := adapter.Call("org.bluez.Adapter1.StartDiscovery", 0); call.Err != nil {
        return fmt.Errorf("failed to start discovery: %w", call.Err)
    }
    // Ensure we stop discovery on context cancel.
    stopOnce := sync.Once{}
    stop := func() {
        stopOnce.Do(func() {
            _ = adapter.Call("org.bluez.Adapter1.StopDiscovery", 0).Err
        })
    }
    // Subscribe to InterfacesAdded and PropertiesChanged signals to learn
    // about new devices and changes to existing ones.
    signals := make(chan *dbus.Signal, 10)
    conn.Signal(signals)
    // match ObjectManager InterfacesAdded
    matchIA := dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager")
    matchSender := dbus.WithMatchSender("org.bluez")
    matchProps := dbus.WithMatchInterface("org.freedesktop.DBus.Properties")
    _ = conn.AddMatchSignal(matchSender, matchIA, dbus.WithMatchMember("InterfacesAdded"))
    _ = conn.AddMatchSignal(matchSender, matchProps, dbus.WithMatchMember("PropertiesChanged"))
    // Track discovered devices to avoid duplicate prints.
    discovered := make(map[dbus.ObjectPath]DiscoveredDevice)
    // Helper to process device properties and maybe invoke callback.
    processProps := func(path dbus.ObjectPath, props map[string]dbus.Variant) {
        // We are only interested in devices exposing service data.
        sdVar, ok := props["ServiceData"]
        if !ok {
            return
        }
        // ServiceData is a dict with UUID strings as keys and byte arrays as values.
        svcMap, ok := sdVar.Value().(map[string]dbus.Variant)
        if !ok {
            return
        }
        var devID, devName string
        var supports5 bool
        var brandName string
        for uuidStr, v := range svcMap {
            data, ok := v.Value().([]byte)
            if !ok {
                continue
            }
            // Determine flags from UUID.
            s5, brandId := parseUUIDFlags(uuidStr)
            // Attempt to parse ID and name from value.
            id, name, ok := parseServiceData(uuidStr, data)
            if ok {
                devID = id
                devName = name
                supports5 = s5
                if bn := brandNameById(brandId); bn != "" {
                    brandName = bn
                }
            }
        }
        if devID == "" || devName == "" {
            return
        }
        dev := DiscoveredDevice{
            ID:           devID,
            Name:         devName,
            Brand:        brandName,
            Supports5Ghz: supports5,
        }
        prev, exists := discovered[path]
        if !exists || prev != dev {
            discovered[path] = dev
            callback(dev)
        }
    }
    // Kick off a goroutine to listen for signals.
    go func() {
        for {
            select {
            case <-ctx.Done():
                stop()
                return
            case sig := <-signals:
                if sig == nil {
                    continue
                }
                switch sig.Name {
                case "org.freedesktop.DBus.ObjectManager.InterfacesAdded":
                    // Arguments: object path, dict of interfaces→dict of properties
                    if len(sig.Body) != 2 {
                        continue
                    }
                    path, _ := sig.Body[0].(dbus.ObjectPath)
                    ifaces, ok := sig.Body[1].(map[string]map[string]dbus.Variant)
                    if !ok {
                        continue
                    }
                    props, ok := ifaces["org.bluez.Device1"]
                    if ok {
                        processProps(path, props)
                    }
                case "org.freedesktop.DBus.Properties.PropertiesChanged":
                    // Arguments: interface name, dict of changed properties, array of invalidated
                    if len(sig.Body) < 3 {
                        continue
                    }
                    ifaceName, _ := sig.Body[0].(string)
                    if ifaceName != "org.bluez.Device1" {
                        continue
                    }
                    path := sig.Path
                    changed, ok := sig.Body[1].(map[string]dbus.Variant)
                    if !ok {
                        continue
                    }
                    if _, ok := changed["ServiceData"]; ok {
                        processProps(path, changed)
                    }
                }
            }
        }
    }()
    // Wait for cancellation.
    <-ctx.Done()
    stop()
    // Remove match rules and discard signals.
    _ = conn.RemoveMatchSignal(matchSender, matchIA, dbus.WithMatchMember("InterfacesAdded"))
    _ = conn.RemoveMatchSignal(matchSender, matchProps, dbus.WithMatchMember("PropertiesChanged"))
    return nil
}
