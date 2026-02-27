package ble

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	gattServiceUUID = "00009955-0000-1000-8000-00805f9b34fb"
	charStatusUUID  = "00009954-0000-1000-8000-00805f9b34fb"
	charP2PUUID     = "00009953-0000-1000-8000-00805f9b34fb"
)

type DeviceInfo struct {
	State    int     `json:"state"`
	Key      *string `json:"key"`
	Mac      string  `json:"mac"`
	CatShare *int    `json:"catShare,omitempty"`
}

type P2pInfo struct {
	ID       *string `json:"id"`
	Ssid     string  `json:"ssid"`
	Psk      string  `json:"psk"`
	Mac      string  `json:"mac"`
	Port     int     `json:"port"`
	Key      *string `json:"key"`
	CatShare *int    `json:"catShare,omitempty"`
}

type gattApp struct {
	conn *dbus.Conn
	// callbacks
	onP2P func(P2pInfo)
}

type gattService struct {
	Path    dbus.ObjectPath
	UUID    string
	Primary bool
}

type gattChar struct {
	Path    dbus.ObjectPath
	UUID    string
	Service dbus.ObjectPath
	Flags   []string

	// status payload provider
	readFn func(offset int) ([]byte, error)

	// write handler
	writeFn func(value []byte, offset int) error

	mu  sync.Mutex
	buf []byte // 用于拼包
}

func StartGattServer(ctx context.Context, onP2P func(P2pInfo)) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}

	appPath := dbus.ObjectPath("/org/orshare/gatt")
	svcPath := dbus.ObjectPath("/org/orshare/gatt/service0")
	charStatusPath := dbus.ObjectPath("/org/orshare/gatt/service0/char_status")
	charP2PPath := dbus.ObjectPath("/org/orshare/gatt/service0/char_p2p")

	app := &gattApp{conn: conn, onP2P: onP2P}
	svc := &gattService{Path: svcPath, UUID: gattServiceUUID, Primary: true}

	// build status JSON
	statusBytes := func() []byte {
		pub := EncodedPublicKeyB64()
		cs := 1 // 随便先填个非空；你也可以填 OrShare 自己的版本号
		info := DeviceInfo{
			State:    0,
			Key:      &pub,
			Mac:      "02:00:00:00:00:00", // 先占位；后面你可以从 p2p0/wlan0 读真实 MAC
			CatShare: &cs,
		}
		b, _ := json.Marshal(info)
		return b
	}

	charStatus := &gattChar{
		Path:    charStatusPath,
		UUID:    charStatusUUID,
		Service: svcPath,
		Flags:   []string{"read"},
		readFn: func(offset int) ([]byte, error) {
			b := statusBytes()
			if offset >= len(b) {
				return []byte{}, nil
			}
			return b[offset:], nil
		},
	}

	charP2P := &gattChar{
		Path:    charP2PPath,
		UUID:    charP2PUUID,
		Service: svcPath,
		Flags:   []string{"write", "write-without-response"},
		writeFn: func(value []byte, offset int) error {
			// BlueZ 可能分片写（带 offset），这里简单起见只处理 offset=0；需要的话可缓存拼包
			if offset != 0 {
				// 先别 silently fail
				return fmt.Errorf("offset write not supported: %d", offset)
			}
			var p P2pInfo
			if err := json.Unmarshal(value, &p); err != nil {
				return err
			}
			// 如果有 key，解密 ssid/psk/mac（尝试多种派生方式，选能解出可打印文本的一种）
			if p.Key != nil {
				session, err := DeriveSessionCipher(*p.Key)
				if err != nil {
					return err
				}

				ssid, err := session.DecryptB64(p.Ssid)
				if err != nil {
					return err
				}

				psk, err := session.DecryptB64(p.Psk)
				if err != nil {
					return err
				}

				mac, err := session.DecryptB64(p.Mac)
				if err != nil {
					return err
				}

				p.Ssid, p.Psk, p.Mac = ssid, psk, mac
				p.Key = nil

				fmt.Printf("[P2P] decrypted: ssid=%q psk=%q mac=%q port=%d\n", p.Ssid, p.Psk, p.Mac, p.Port)
			}
			if app.onP2P != nil {
				app.onP2P(p)
			}
			return nil
		},
	}

	// Export objects
	conn.Export(app, appPath, "org.freedesktop.DBus.ObjectManager")
	conn.Export(svc, svcPath, "org.bluez.GattService1")
	conn.Export(charStatus, charStatusPath, "org.bluez.GattCharacteristic1")
	conn.Export(charP2P, charP2PPath, "org.bluez.GattCharacteristic1")

	// RegisterApplication on hci0
	gm := conn.Object("org.bluez", "/org/bluez/hci0")
	if call := gm.Call("org.bluez.GattManager1.RegisterApplication", 0, appPath, map[string]dbus.Variant{}); call.Err != nil {
		return fmt.Errorf("RegisterApplication: %w", call.Err)
	}

	fmt.Println("GATT server registered")

	go func() {
		<-ctx.Done()
		_ = gm.Call("org.bluez.GattManager1.UnregisterApplication", 0, appPath).Err
	}()

	return nil
}

// ---- ObjectManager ----

// GetManagedObjects returns the whole GATT object tree (BlueZ uses this to read UUID/Flags/Service links)
func (a *gattApp) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}

	// service
	svcPath := dbus.ObjectPath("/org/orshare/gatt/service0")
	objs[svcPath] = map[string]map[string]dbus.Variant{
		"org.bluez.GattService1": {
			"UUID":    dbus.MakeVariant(gattServiceUUID),
			"Primary": dbus.MakeVariant(true),
		},
	}

	// status char
	cs := dbus.ObjectPath("/org/orshare/gatt/service0/char_status")
	objs[cs] = map[string]map[string]dbus.Variant{
		"org.bluez.GattCharacteristic1": {
			"UUID":    dbus.MakeVariant(charStatusUUID),
			"Service": dbus.MakeVariant(svcPath),
			"Flags":   dbus.MakeVariant([]string{"read"}),
		},
	}

	// p2p char
	cp := dbus.ObjectPath("/org/orshare/gatt/service0/char_p2p")
	objs[cp] = map[string]map[string]dbus.Variant{
		"org.bluez.GattCharacteristic1": {
			"UUID":    dbus.MakeVariant(charP2PUUID),
			"Service": dbus.MakeVariant(svcPath),
			"Flags":   dbus.MakeVariant([]string{"write", "write-without-response"}),
		},
	}

	return objs, nil
}

// ---- Characteristics ----

func (c *gattChar) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	if c.readFn == nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("not readable"))
	}
	offset := 0
	if v, ok := options["offset"]; ok {
		if o, ok2 := v.Value().(uint16); ok2 {
			offset = int(o)
		}
	}
	b, err := c.readFn(offset)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return b, nil
}

func (c *gattChar) WriteValue(value []byte, options map[string]dbus.Variant) *dbus.Error {
	if c.writeFn == nil {
		return dbus.MakeFailedError(fmt.Errorf("not writable"))
	}

	offset := 0
	if v, ok := options["offset"]; ok {
		if o, ok2 := v.Value().(uint16); ok2 {
			offset = int(o)
		}
	}

	// 🔥 加这三行
	fmt.Printf("[GATT] WriteValue uuid=%s offset=%d len=%d\n", c.UUID, offset, len(value))
	fmt.Printf("[GATT] data=%s\n", string(value))
	fmt.Println("----------------------------------------------------")

	if err := c.writeFn(value, offset); err != nil {
		fmt.Printf("[GATT] writeFn error: %v\n", err)
		return dbus.MakeFailedError(err)
	}
	return nil
}
