package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	imagedraw "image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/go-toast/toast"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	VID           = 0x1038
	PID           = 0x226D
	IFACE_BATTERY = 3
	EP_BATTERY    = 0x81
	IFACE_EVENTS  = 5
	EP_EVENTS     = 0x84
	POLL_INTERVAL = 30 * time.Second
	HEADSET_NAME  = "Arctis Nova 3X Wireless"
	// Change this if you want to use a libusb dll from a specific user's Python install.
	PYTHON_USER = "" // empty for current user
)

var (
	batteryCmd = bytes.Repeat([]byte{0x00}, 32)

	logFile *os.File

	usbMu  sync.Mutex
	usbCtx uintptr
	usbDev uintptr
	libusb *libusbAPI
	app    = &batteryTray{battery: -1, prevBattery: -1, connected: true}
)

type libusbAPI struct {
	dll                   *syscall.LazyDLL
	initProc              *syscall.LazyProc
	exitProc              *syscall.LazyProc
	openByVidPidProc      *syscall.LazyProc
	closeProc             *syscall.LazyProc
	claimInterfaceProc    *syscall.LazyProc
	releaseInterfaceProc  *syscall.LazyProc
	controlTransferProc   *syscall.LazyProc
	interruptTransferProc *syscall.LazyProc
}

type batteryTray struct {
	mu          sync.Mutex
	battery     int
	prevBattery int
	hasPrev     bool
	muted       bool
	connected   bool
	flashing    bool
	flashState  bool
}

var (
	fontFace font.Face
	fontOnce sync.Once
)

func init() {
	batteryCmd[0] = 0xB0
	f, err := os.OpenFile("debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		log.SetOutput(f)
		logFile = f
	}
}

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	log.Println("App started - exact libusb port")
	systray.SetTitle(HEADSET_NAME)

	app.battery = readBattery()
	app.prevBattery = app.battery
	app.hasPrev = true
	app.refreshIcon(false)

	refreshItem := systray.AddMenuItem("Refresh", "Refresh battery now")
	quitItem := systray.AddMenuItem("Quit", "Quit the app")

	go func() {
		for range refreshItem.ClickedCh {
			pct := readBattery()
			app.mu.Lock()
			app.battery = pct
			app.mu.Unlock()
			app.refreshIcon(false)
		}
	}()

	go func() {
		<-quitItem.ClickedCh
		app.stopFlashing()
		systray.Quit()
	}()

	go app.updateLoop()
	go app.eventLoop()
}

func onExit() {
	usbMu.Lock()
	if libusb != nil {
		if usbDev != 0 {
			libusb.close(usbDev)
			usbDev = 0
		}
		if usbCtx != 0 {
			libusb.exit(usbCtx)
			usbCtx = 0
		}
	}
	usbMu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
	}
}

func resolveLibusbPath() string {
	candidates := []string{
		filepath.Join(filepath.Dir(os.Args[0]), "libusb-1.0.dll"),
		filepath.Join(`C:\Users`, PYTHON_USER, `AppData\Local\Programs\Python\Python311\Lib\site-packages\usb1\libusb-1.0.dll`),
		"libusb-1.0.dll",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func loadLibusb() (*libusbAPI, error) {
	if libusb != nil {
		return libusb, nil
	}
	path := resolveLibusbPath()
	if path == "" {
		return nil, fmt.Errorf("libusb-1.0.dll not found")
	}
	dll := syscall.NewLazyDLL(path)
	api := &libusbAPI{
		dll:                   dll,
		initProc:              dll.NewProc("libusb_init"),
		exitProc:              dll.NewProc("libusb_exit"),
		openByVidPidProc:      dll.NewProc("libusb_open_device_with_vid_pid"),
		closeProc:             dll.NewProc("libusb_close"),
		claimInterfaceProc:    dll.NewProc("libusb_claim_interface"),
		releaseInterfaceProc:  dll.NewProc("libusb_release_interface"),
		controlTransferProc:   dll.NewProc("libusb_control_transfer"),
		interruptTransferProc: dll.NewProc("libusb_interrupt_transfer"),
	}
	if err := dll.Load(); err != nil {
		return nil, err
	}
	libusb = api
	log.Printf("Loaded libusb from %s", path)
	return api, nil
}

func (l *libusbAPI) init(ctx *uintptr) int {
	ret, _, _ := l.initProc.Call(uintptr(unsafe.Pointer(ctx)))
	return int(int32(ret))
}

func (l *libusbAPI) exit(ctx uintptr) {
	l.exitProc.Call(ctx)
}

func (l *libusbAPI) openByVIDPID(ctx uintptr, vid, pid uint16) uintptr {
	ret, _, _ := l.openByVidPidProc.Call(ctx, uintptr(vid), uintptr(pid))
	return ret
}

func (l *libusbAPI) close(dev uintptr) {
	l.closeProc.Call(dev)
}

func (l *libusbAPI) claimInterface(dev uintptr, iface int) int {
	ret, _, _ := l.claimInterfaceProc.Call(dev, uintptr(iface))
	return int(int32(ret))
}

func (l *libusbAPI) releaseInterface(dev uintptr, iface int) int {
	ret, _, _ := l.releaseInterfaceProc.Call(dev, uintptr(iface))
	return int(int32(ret))
}

func (l *libusbAPI) controlTransfer(dev uintptr, requestType, request uint8, value, index uint16, data []byte, timeout uint32) int {
	var ptr uintptr
	if len(data) > 0 {
		ptr = uintptr(unsafe.Pointer(&data[0]))
	}
	ret, _, _ := l.controlTransferProc.Call(
		dev,
		uintptr(requestType),
		uintptr(request),
		uintptr(value),
		uintptr(index),
		ptr,
		uintptr(len(data)),
		uintptr(timeout),
	)
	return int(int32(ret))
}

func (l *libusbAPI) interruptTransfer(dev uintptr, endpoint uint8, data []byte, transferred *int32, timeout uint32) int {
	var ptr uintptr
	if len(data) > 0 {
		ptr = uintptr(unsafe.Pointer(&data[0]))
	}
	ret, _, _ := l.interruptTransferProc.Call(
		dev,
		uintptr(endpoint),
		ptr,
		uintptr(len(data)),
		uintptr(unsafe.Pointer(transferred)),
		uintptr(timeout),
	)
	return int(int32(ret))
}

func getDevice() uintptr {
	api, err := loadLibusb()
	if err != nil {
		log.Printf("loadLibusb failed: %v", err)
		return 0
	}
	if usbDev != 0 {
		return usbDev
	}
	if usbCtx == 0 {
		if rc := api.init(&usbCtx); rc != 0 {
			log.Printf("libusb_init failed: %d", rc)
			return 0
		}
	}
	usbDev = api.openByVIDPID(usbCtx, VID, PID)
	if usbDev == 0 {
		log.Printf("openByVendorIDAndProductID failed for vid=%#x pid=%#x", VID, PID)
		return 0
	}
	return usbDev
}

func resetDevice() {
	if libusb != nil && usbDev != 0 {
		libusb.close(usbDev)
	}
	usbDev = 0
}

func readBattery() int {
	usbMu.Lock()
	defer usbMu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("readBattery panic: %v", r)
				}
			}()
		}()

		dev := getDevice()
		if dev == 0 {
			return -1
		}
		if rc := libusb.claimInterface(dev, IFACE_BATTERY); rc != 0 {
			log.Printf("claimInterface failed: %d", rc)
			resetDevice()
			if attempt == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return -1
		}

		controlRC := libusb.controlTransfer(dev, 0x21, 0x09, 0x0200, IFACE_BATTERY, batteryCmd, 1000)
		if controlRC < 0 {
			_ = libusb.releaseInterface(dev, IFACE_BATTERY)
			log.Printf("controlTransfer failed: %d", controlRC)
			resetDevice()
			if attempt == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return -1
		}

		time.Sleep(150 * time.Millisecond)

		buf := make([]byte, 64)
		var transferred int32
		readRC := libusb.interruptTransfer(dev, EP_BATTERY, buf, &transferred, 1000)
		_ = libusb.releaseInterface(dev, IFACE_BATTERY)
		if readRC < 0 {
			log.Printf("interruptTransfer failed: %d", readRC)
			resetDevice()
			if attempt == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return -1
		}
		if transferred >= 4 && buf[0] == 0xB0 && buf[3] <= 100 {
			return int(buf[3])
		}
		return -1
	}
	return -1
}

func sendNotification(title, message string) {
	notification := toast.Notification{
		AppID:   HEADSET_NAME,
		Title:   title,
		Message: message,
	}
	_ = notification.Push()
}

func (b *batteryTray) tooltip() string {
	if !b.connected || b.battery < 0 {
		return HEADSET_NAME + " - disconnected"
	}
	muteStr := "Unmuted"
	if b.muted {
		muteStr = "Muted"
	}
	return fmt.Sprintf("%s - %d%%  |  %s", HEADSET_NAME, b.battery, muteStr)
}

func (b *batteryTray) refreshIcon(flashState bool) {
	b.mu.Lock()
	percent := b.battery
	muted := b.muted
	b.mu.Unlock()
	iconData := makeIconICO(percent, muted, flashState)
	if len(iconData) > 0 {
		systray.SetIcon(iconData)
	}
	systray.SetTooltip(b.tooltip())
}

func (b *batteryTray) startFlashing() {
	b.mu.Lock()
	if b.flashing {
		b.mu.Unlock()
		return
	}
	b.flashing = true
	b.mu.Unlock()

	go func() {
		for {
			b.mu.Lock()
			shouldFlash := b.flashing && b.battery >= 0 && b.battery <= 10
			if shouldFlash {
				b.flashState = !b.flashState
			}
			flashState := b.flashState
			b.mu.Unlock()

			if !shouldFlash {
				b.mu.Lock()
				b.flashing = false
				b.flashState = false
				b.mu.Unlock()
				b.refreshIcon(false)
				return
			}

			b.refreshIcon(flashState)
			time.Sleep(600 * time.Millisecond)
		}
	}()
}

func (b *batteryTray) stopFlashing() {
	b.mu.Lock()
	b.flashing = false
	b.flashState = false
	b.mu.Unlock()
}

func (b *batteryTray) checkNotifications(newPct int) {
	b.mu.Lock()
	prev := b.prevBattery
	hasPrev := b.hasPrev
	b.mu.Unlock()

	if !hasPrev || newPct < 0 {
		return
	}
	if prev >= 20 && newPct < 20 {
		sendNotification(
			"⚠️ "+HEADSET_NAME+" battery low",
			fmt.Sprintf("Battery at %d%% - plug in soon!", newPct),
		)
	}
	if prev >= 10 && newPct < 10 {
		sendNotification(
			"🔴 "+HEADSET_NAME+" critically low",
			fmt.Sprintf("Battery at %d%% - plug in now!", newPct),
		)
	}
}

func (b *batteryTray) updateLoop() {
	for {
		pct := readBattery()
		b.checkNotifications(pct)

		b.mu.Lock()
		b.battery = pct
		b.prevBattery = pct
		b.hasPrev = true
		needFlash := pct >= 0 && pct <= 10
		b.mu.Unlock()

		if needFlash {
			b.startFlashing()
		} else {
			b.stopFlashing()
			b.refreshIcon(false)
		}
		time.Sleep(POLL_INTERVAL)
	}
}

func (b *batteryTray) eventLoop() {
	for {
		usbMu.Lock()
		dev := getDevice()
		if dev == 0 {
			usbMu.Unlock()
			time.Sleep(2 * time.Second)
			continue
		}
		if rc := libusb.claimInterface(dev, IFACE_EVENTS); rc != 0 {
			log.Printf("claimInterface events failed: %d", rc)
			resetDevice()
			usbMu.Unlock()
			time.Sleep(2 * time.Second)
			continue
		}
		usbMu.Unlock()

		for {
			buf := make([]byte, 64)
			var transferred int32
			rc := libusb.interruptTransfer(dev, EP_EVENTS, buf, &transferred, 2000)
			if rc < 0 {
				break
			}
			if transferred == 0 {
				continue
			}

			cmd := buf[0]
			switch cmd {
			case 0x52:
				b.mu.Lock()
				b.muted = buf[2] == 0x00
				b.mu.Unlock()
				b.refreshIcon(false)
			case 0xAA:
				b.mu.Lock()
				b.connected = buf[1] == 0x01
				b.mu.Unlock()
				b.refreshIcon(false)
			}
		}

		usbMu.Lock()
		if libusb != nil && dev != 0 {
			_ = libusb.releaseInterface(dev, IFACE_EVENTS)
		}
		resetDevice()
		usbMu.Unlock()
		time.Sleep(2 * time.Second)
	}
}

func getFontFace() font.Face {
	fontOnce.Do(func() {
		fontPaths := []string{
			filepath.Join(os.Getenv("WINDIR"), "Fonts", "arialbd.ttf"),
			filepath.Join(os.Getenv("WINDIR"), "Fonts", "arial.ttf"),
			filepath.Join(os.Getenv("WINDIR"), "Fonts", "verdana.ttf"),
		}
		for _, p := range fontPaths {
			fontData, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			f, err := opentype.Parse(fontData)
			if err != nil {
				continue
			}
			fontFace, _ = opentype.NewFace(f, &opentype.FaceOptions{
				Size:    58,
				DPI:     72,
				Hinting: font.HintingFull,
			})
			if fontFace != nil {
				return
			}
		}
	})
	return fontFace
}

func makeIconICO(percent int, muted bool, flash bool) []byte {
	const size = 128
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	var bg color.RGBA
	text := "?"
	switch {
	case percent < 0:
		bg = color.RGBA{80, 80, 80, 255}
	case percent <= 10 && flash:
		bg = color.RGBA{120, 20, 20, 255}
		text = fmt.Sprintf("%d", percent)
	case percent <= 10:
		bg = color.RGBA{210, 40, 40, 255}
		text = fmt.Sprintf("%d", percent)
	case percent <= 20:
		bg = color.RGBA{210, 40, 40, 255}
		text = fmt.Sprintf("%d", percent)
	case percent <= 40:
		bg = color.RGBA{220, 140, 0, 255}
		text = fmt.Sprintf("%d", percent)
	default:
		bg = color.RGBA{30, 180, 70, 255}
		text = fmt.Sprintf("%d", percent)
	}
	if muted {
		bg = color.RGBA{70, 90, 110, 255}
	}

	drawRoundedRect(img, bg)

	if muted {
		drawMutedBar(img)
	}

	face := getFontFace()
	if face != nil {
		drawer := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.RGBA{0, 0, 0, 120}),
			Face: face,
		}
		bounds, _ := drawer.BoundString(text)
		tw := (bounds.Max.X - bounds.Min.X).Ceil()
		th := (bounds.Max.Y - bounds.Min.Y).Ceil()
		x := (size - tw) / 2
		y := (size+th)/2 - 6
		if muted {
			y -= 6
		}

		drawer.Dot = fixed.P(x+2, y+2)
		drawer.DrawString(text)

		drawer.Src = image.NewUniform(color.White)
		drawer.Dot = fixed.P(x, y)
		drawer.DrawString(text)
	}

	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	pngData := pngBuf.Bytes()

	icoBuf := new(bytes.Buffer)
	_ = binary.Write(icoBuf, binary.LittleEndian, uint16(0))
	_ = binary.Write(icoBuf, binary.LittleEndian, uint16(1))
	_ = binary.Write(icoBuf, binary.LittleEndian, uint16(1))
	icoBuf.WriteByte(0)
	icoBuf.WriteByte(0)
	icoBuf.WriteByte(0)
	icoBuf.WriteByte(0)
	_ = binary.Write(icoBuf, binary.LittleEndian, uint16(1))
	_ = binary.Write(icoBuf, binary.LittleEndian, uint16(32))
	_ = binary.Write(icoBuf, binary.LittleEndian, uint32(len(pngData)))
	_ = binary.Write(icoBuf, binary.LittleEndian, uint32(22))
	_, _ = icoBuf.Write(pngData)
	return icoBuf.Bytes()
}

func drawRoundedRect(img *image.RGBA, fill color.RGBA) {
	const (
		size   = 128
		radius = 22
		inset  = 3
	)
	uniform := &image.Uniform{C: fill}
	imagedraw.Draw(img, image.Rect(inset+radius, inset, size-inset-radius, size-inset), uniform, image.Point{}, imagedraw.Src)
	imagedraw.Draw(img, image.Rect(inset, inset+radius, size-inset, size-inset-radius), uniform, image.Point{}, imagedraw.Src)

	r2 := radius * radius
	for y := 0; y < radius; y++ {
		for x := 0; x < radius; x++ {
			dx := radius - 1 - x
			dy := radius - 1 - y
			if dx*dx+dy*dy <= r2 {
				img.Set(inset+x, inset+y, fill)
				img.Set(size-inset-radius+x, inset+y, fill)
				img.Set(inset+x, size-inset-radius+y, fill)
				img.Set(size-inset-radius+x, size-inset-radius+y, fill)
			}
		}
	}
}

func drawMutedBar(img *image.RGBA) {
	fill := color.RGBA{200, 60, 60, 255}
	for y := 106; y < 118; y++ {
		for x := 12; x < 116; x++ {
			img.Set(x, y, fill)
		}
	}
}
