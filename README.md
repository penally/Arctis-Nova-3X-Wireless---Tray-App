# Arctis Nova 3X Wireless - Tray App

A lightweight Windows system tray app that shows your **SteelSeries Arctis Nova 3X Wireless** headset's battery percentage and mute state in real time, without SteelSeries GG.

Built by reverse engineering the USB HID/libusb protocol directly from the 2.4GHz dongle.

**Important:** this is now a **Go app**, not a Python script.

- You do **not** need Python installed to run it.
- You **do** still need `libusb-1.0.dll`.
- Easiest setup: install it with `pip` by running `py -3.11 -m pip install libusb1`.

I kept using a python package in a go script for reliability. and was easier to mantain with that.

---

## Features

- `Live battery %` shown as a colour-coded tray icon
  - Green = above 40%
  - Orange = 21-40%
  - Red = 20% and below
  - Flashing red = critically low (10% and below)
- `Mute indicator` - icon turns grey-blue with a red bar when muted
- `Windows notifications`
  - Warning at 20%: "plug in soon"
  - Critical alert at 10%: "plug in now"
- `Hover tooltip` shows headset name, battery %, and mute state
- Right-click menu: `Refresh` and `Quit`
- Polls battery every 30 seconds; mute updates instantly
- Runs in the background with no console window

---

## Requirements

- Windows 10 or 11
- 64-bit Windows recommended for the current build
- The `2.4GHz USB dongle` plugged in
- `Zadig` for one-time driver setup: [https://zadig.akeo.ie](https://zadig.akeo.ie)
- `libusb-1.0.dll` available on your machine
- Easiest way to get it:

```powershell
py -3.11 -m pip install libusb1
```

If you want to build from source:

- Go 1.26+

---

## Setup

### 1. Set up Zadig drivers

The app needs WinUSB drivers on two interfaces to read from the dongle.

1. Download and run **Zadig** as Administrator from [https://zadig.akeo.ie](https://zadig.akeo.ie)
2. Go to `Options -> List All Devices`
3. Select `Arctis Nova 3X Wireless (Interface 3)`
4. Set the right-side driver to `WinUSB`
5. Click `Replace Driver` and wait
6. Repeat for `Arctis Nova 3X Wireless (Interface 5)`

Note: This only affects the control interfaces, not the audio path. Your headset audio should continue to work normally. To undo, open Device Manager, find the interface, uninstall it, then replug the dongle.

### 2. Build from source (optional)

From this folder:

```powershell
go build -ldflags="-H=windowsgui -s -w" -o arctis_battery.exe
```

This builds the tray app as a background Windows GUI app with no visible console window.

---

## Running

You do **not** need Python for this.

Run the exe:

```powershell
.\arctis_battery.exe
```

The tray icon appears in your system tray showing the current battery percentage.

If you installed `libusb1` with `pip`, that is usually enough.

If USB access fails on your machine, try running it as Administrator.

---

## Run At Startup

To start it automatically at login, use a scheduled task.



## USB Protocol

| Interface | Endpoint | Direction | Purpose |
|-----------|----------|-----------|---------|
| 3 | `0x81` | IN | Battery polling |
| 5 | `0x84` | IN | Mute and connection events |

**Battery query**

Send `[0xB0, 0x00 x 31]` to Interface 3, then read the response:

- `data[0] = 0xB0` confirms battery response
- `data[3] = battery % (0-100)`

**Mute events**

Listen on Interface 5:

- `data[0] = 0x52` means mute button event
- `data[2] = 0x00` means muted
- `data[2] = 0x01` means unmuted

**Connection events**

Listen on Interface 5:

- `data[0] = 0xAA`
- `data[1] = 0x01` means connected
- `data[1] = 0x00` means disconnected



---

## Troubleshooting

**Icon shows `?`**

- Make sure the dongle is plugged in
- Make sure Zadig was applied to Interface 3
- Make sure `libusb-1.0.dll` is available to the app
- Try running the app as Administrator

**Mute state doesn't update**

- Check Zadig and make sure Interface 5 also uses `WinUSB`

**Audio stops working after Zadig**

- Open Device Manager
- Find the affected interface
- Right-click `Uninstall device`
- Replug the dongle so Windows restores the original driver

**App doesn't start**

- Make sure `libusb-1.0.dll` is available
- Easiest fix:

```powershell
py -3.11 -m pip install libusb1
```

- Check that you're using the built [arctis_battery.exe](file:///c:/Users/uira/Downloads/thing/arctis_battery.exe)

---

## I dont know if this works for any other headset I don't take any responsibility for anything breaking your headset.

## Licence

MIT - do whatever you want with it.
