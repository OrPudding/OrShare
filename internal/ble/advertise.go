package ble

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	// ADV_SERVICE_UUID matches BleUtils.ADV_SERVICE_UUID used by CatShare.
	advServiceUUID = "00003331-0000-1000-8000-008123456789"

	// Bluetooth SIG base UUID (for 16-bit UUID expansion)
	btBaseUUID = "00000000-0000-1000-8000-00805f9b34fb"
)

type Advertisement struct {
	Path dbus.ObjectPath
	Type string // "peripheral"

	// --- Newer BlueZ fields (raw AD structures)
	Data             map[uint8]dbus.Variant
	ScanResponseData map[uint8]dbus.Variant

	// --- Widely supported BlueZ fields
	ServiceUUIDs []string
	ServiceData  map[string]dbus.Variant
	LocalName    string

	Includes []string
}

func (a *Advertisement) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != "org.bluez.LEAdvertisement1" {
		return nil, dbus.MakeFailedError(fmt.Errorf("unknown iface: %s", iface))
	}

	props := map[string]dbus.Variant{
		"Type": dbus.MakeVariant(a.Type),
	}

	// Only include properties that are actually set.
	// (Older BlueZ may choke on unknown properties like ScanResponseData/Data)
	if len(a.ServiceUUIDs) > 0 {
		props["ServiceUUIDs"] = dbus.MakeVariant(a.ServiceUUIDs)
	}
	if len(a.ServiceData) > 0 {
		props["ServiceData"] = dbus.MakeVariant(a.ServiceData)
	}
	if a.LocalName != "" {
		props["LocalName"] = dbus.MakeVariant(a.LocalName)
	}
	if len(a.Includes) > 0 {
		props["Includes"] = dbus.MakeVariant(a.Includes)
	}

	// Raw payload mode
	if len(a.Data) > 0 {
		props["Data"] = dbus.MakeVariant(a.Data)
	}
	if len(a.ScanResponseData) > 0 {
		props["ScanResponseData"] = dbus.MakeVariant(a.ScanResponseData)
	}

	return props, nil
}

func (a *Advertisement) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	all, derr := a.GetAll(iface)
	if derr != nil {
		return dbus.Variant{}, derr
	}
	v, ok := all[prop]
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("unknown property: %s", prop))
	}
	return v, nil
}

func (a *Advertisement) Set(iface, prop string, value dbus.Variant) *dbus.Error {
	return dbus.MakeFailedError(fmt.Errorf("read-only"))
}

func (a *Advertisement) Release() *dbus.Error { return nil }

// sd1 (6 bytes): [senderId(2)] + [0,0,0,0]
// sd2 (27 bytes):
//
//	bytes 0..7   = 0
//	bytes 8..9   = senderId(2)
//	bytes 10..25 = device name (UTF-8), truncated to <=15 bytes; if truncated append '\t'
//	byte  26     = 0x01
func buildServiceData(senderId []byte, name string) (sd1 []byte, sd2 []byte) {
	sd1 = make([]byte, 6)
	copy(sd1, senderId)

	sd2 = make([]byte, 27)
	copy(sd2[8:], senderId)

	nameBytes := []byte(name)
	truncated := nameBytes
	truncatedWithTab := false

	if len(nameBytes) > 15 {
		// cut to <=15 bytes without splitting UTF-8 rune
		n := 0
		for n < len(nameBytes) {
			_, size := utf8.DecodeRune(nameBytes[n:])
			if n+size > 15 {
				break
			}
			n += size
		}
		truncated = nameBytes[:n]
		truncatedWithTab = true
	}

	if len(truncated) > 16 {
		truncated = truncated[:16]
	}

	copy(sd2[10:], truncated)

	if truncatedWithTab {
		idx := 10 + len(truncated)
		if idx < 26 {
			sd2[idx] = '\t'
		}
	}

	sd2[26] = 1
	return sd1, sd2
}

func randomSenderId() ([]byte, error) {
	b := make([]byte, 2)
	_, err := rand.Read(b)
	return b, err
}

// uuid128Raw returns 16 bytes big-endian from canonical UUID string.
func uuid128Raw(uuidStr string) ([]byte, error) {
	s := strings.ReplaceAll(uuidStr, "-", "")
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(raw) != 16 {
		return nil, fmt.Errorf("bad uuid length")
	}
	return raw, nil
}

// uuid128ToAdvLE converts canonical UUID bytes (big-endian) to AD little-endian byte order
// used in 0x07 list. (BlueZ raw Data expects the *payload bytes*, not the string.)
func uuid128ToAdvLE(uuidStr string) ([]byte, error) {
	raw, err := uuid128Raw(uuidStr)
	if err != nil {
		return nil, err
	}
	// AD payload for 128-bit UUID list uses little-endian (reverse 16 bytes).
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}
	return raw, nil
}

func uuid16ToFull(uuid16 uint16) string {
	// Expand 16-bit into base UUID
	// 0000xxxx-0000-1000-8000-00805f9b34fb
	return fmt.Sprintf("0000%04x%s", uuid16, btBaseUUID[8:])
}

// export introspection so busctl can see methods (optional but helpful)
func exportIntrospection(conn *dbus.Conn, path dbus.ObjectPath) {
	node := &introspect.Node{
		Name: string(path),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{
				Name: "org.bluez.LEAdvertisement1",
				Methods: []introspect.Method{
					{Name: "Release"},
				},
			},
			{
				Name: "org.freedesktop.DBus.Properties",
				Methods: []introspect.Method{
					{
						Name: "GetAll",
						Args: []introspect.Arg{
							{Name: "interface", Type: "s", Direction: "in"},
							{Name: "props", Type: "a{sv}", Direction: "out"},
						},
					},
					{
						Name: "Get",
						Args: []introspect.Arg{
							{Name: "interface", Type: "s", Direction: "in"},
							{Name: "prop", Type: "s", Direction: "in"},
							{Name: "value", Type: "v", Direction: "out"},
						},
					},
					{
						Name: "Set",
						Args: []introspect.Arg{
							{Name: "interface", Type: "s", Direction: "in"},
							{Name: "prop", Type: "s", Direction: "in"},
							{Name: "value", Type: "v", Direction: "in"},
						},
					},
				},
			},
		},
	}
	conn.Export(introspect.NewIntrospectable(node), path, "org.freedesktop.DBus.Introspectable")
}

// tries to register adv; caller decides whether to retry fallback.
func registerAdv(conn *dbus.Conn, adv *Advertisement) error {
	// Export object + properties
	conn.Export(adv, adv.Path, "org.bluez.LEAdvertisement1")
	conn.Export(adv, adv.Path, "org.freedesktop.DBus.Properties")
	exportIntrospection(conn, adv.Path)

	adapterPath := dbus.ObjectPath("/org/bluez/hci0")
	advmgr := conn.Object("org.bluez", adapterPath)

	call := advmgr.Call("org.bluez.LEAdvertisingManager1.RegisterAdvertisement", 0, adv.Path, map[string]dbus.Variant{})
	if call.Err != nil {
		return call.Err
	}
	return nil
}

func unregisterAdv(conn *dbus.Conn, path dbus.ObjectPath) {
	adapterPath := dbus.ObjectPath("/org/bluez/hci0")
	advmgr := conn.Object("org.bluez", adapterPath)
	_ = advmgr.Call("org.bluez.LEAdvertisingManager1.UnregisterAdvertisement", 0, path).Err
}

// StartAdvertising tries raw Data/ScanResponseData first (newer BlueZ).
// If BlueZ fails to parse, fallback to ServiceUUIDs/ServiceData/LocalName (widely supported).
func StartAdvertising(ctx context.Context, deviceName string) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("failed to connect to system bus: %w", err)
	}

	sid, err := randomSenderId()
	if err != nil {
		return fmt.Errorf("random sender id: %w", err)
	}
	sd1, sd2 := buildServiceData(sid, deviceName)

	// Raw mode values:
	// AD type 0x16 value starts with 16-bit UUID little-endian.
	svc01ff := append([]byte{0xff, 0x01}, sd1...)
	svcffff := append([]byte{0xff, 0xff}, sd2...)

	uuidAdvLE, err := uuid128ToAdvLE(advServiceUUID)
	if err != nil {
		return fmt.Errorf("uuid convert: %w", err)
	}

	// Attempt #1: raw Data + ScanResponseData (best match CatShare)
	rawAdv := &Advertisement{
		Path: dbus.ObjectPath("/org/orshare/advertisement0"),
		Type: "peripheral",
		Data: map[uint8]dbus.Variant{
			0x01: dbus.MakeVariant([]byte{0x02}), // Flags
			0x07: dbus.MakeVariant(uuidAdvLE),    // 128-bit UUID list payload bytes
			0x16: dbus.MakeVariant(svc01ff),      // Service Data 0x01ff + sd1
		},
		ScanResponseData: map[uint8]dbus.Variant{
			0x16: dbus.MakeVariant(svcffff), // Service Data 0xffff + sd2 (scan response)
		},
	}

	if err := registerAdv(conn, rawAdv); err == nil {
		fmt.Printf("BLE advertising registered (raw): name=%q senderId=%02x%02x\n", deviceName, sid[0], sid[1])
		go func() {
			<-ctx.Done()
			unregisterAdv(conn, rawAdv.Path)
		}()
		return nil
	} else {
		// If BlueZ can't parse raw adv, fallback to legacy properties.
		// This is the most common cause of: "Failed to parse advertisement."
		errStr := err.Error()
		if !strings.Contains(errStr, "Failed to parse advertisement") && !errors.Is(err, dbus.ErrMsgUnknownMethod) {
			return fmt.Errorf("failed to register advertisement: %w", err)
		}
	}

	// Attempt #2: legacy mode (widely supported)
	// Note: legacy mode cannot place sd2 into scan response separately.
	// We advertise sd1 via ServiceData(01ff). sd2 is omitted here due to size/format constraints.
	legacyAdv := &Advertisement{
		Path: dbus.ObjectPath("/org/orshare/advertisement1"),
		Type: "peripheral",

		ServiceUUIDs: []string{advServiceUUID},
		ServiceData: map[string]dbus.Variant{
			uuid16ToFull(0x01ff): dbus.MakeVariant(sd1),
		},
		LocalName: deviceName,
	}

	if err := registerAdv(conn, legacyAdv); err != nil {
		return fmt.Errorf("failed to register advertisement (legacy fallback): %w", err)
	}

	fmt.Printf("BLE advertising registered (legacy fallback): name=%q senderId=%02x%02x\n", deviceName, sid[0], sid[1])
	go func() {
		<-ctx.Done()
		unregisterAdv(conn, legacyAdv.Path)
	}()
	return nil
}
