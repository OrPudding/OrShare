# OrShare

OrShare 是一个使用 Go 开发、运行于 Linux 平台的 OEM/CN Share P2P 文件传输实现

通过 BLE + Wi-Fi Direct 实现设备间直连传输，现已加入互传联盟

连接 Wi-Fi Direct 时可能有重试，这是正常现象

保存文件路径：用户下载文件夹/OrShare

---

## 功能

- BLE 设备广播与发现
- 通过 GATT 交换 P2P 信息（SSID / PSK / MAC / Port）
- 自动连接 Wi-Fi Direct（基于 NetworkManager）
- 基于 HTTPS/WSS 的文件接收
- 命令行工具（CLI）

---

## 运行环境

- Linux
- Go 1.20+
- NetworkManager（需要 `nmcli`）
- BlueZ
- 支持 Wi-Fi Direct 的 Wi-Fi 网卡
- 支持 BLE 的蓝牙适配器

---

## 测试环境

| 平台 | 设备 | 系统版本 | 结果 |
|------|------|----------|------|
| Linux | Arch Linux | Rolling | 可发现附近的设备和接收文件 |
| Android | 小米 13 Pro | HyperOS 3 | 可正常发现 Linux 并完成文件发送 |
| Android | 小米 Pad 5 | HyperOS3 | 可正常发现 Linux 并完成文件发送 |

> 注意：Linux 会被 Android 端识别为不定的随机品牌设备名称，不影响实际功能

---

## 编译

```bash
git clone https://github.com/OrPudding/OrShare.git
cd OrShare
go build -o orshare ./cmd/orshare
```

---

## 基础使用

### 启动守护进程（推荐方式）

```bash
./orshare daemon
```

功能：

- 通过 BLE 广播自身
- 等待接收 P2P 信息
- 自动连接 Wi-Fi Direct
- 启动文件接收服务

---

### 扫描附近设备

```bash
./orshare scan
```

---

### 手动接收模式

```bash
./orshare recv --host 192.168.49.1 --port 8443
```

参数说明：

- `--host` 发送端 IP
- `--port` 发送端 HTTPS/WSS 端口
- `--auto-accept` 是否自动接受文件（默认 true）

---

## 工作流程

→ BLE 接收 P2P 凭据（SSID / PSK / MAC / Port）  
→ 连接 Wi-Fi Direct  
→ 解析对端 IP  
→ 启动接收器  

---

## 协议来源

本项目基于 CatShare 的协议与连接流程实现进行重写与适配
属于其衍生实现

CatShare  
https://github.com/kmod-midori/CatShare

原项目作者：Midori Kochiya  
CatShare 使用 MIT License

---

## TODO

- Sender
- GUI
- 更可靠的对端 IP 发现机制
- 更完善的错误处理
- 打包与发行支持

---

## License

MIT
