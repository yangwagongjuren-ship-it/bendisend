# BendiSend · Local File Transfer

**本地传** is a self-hosted tool for sharing text and files between computers and phones on the same local network. One Windows computer hosts a room; other devices join from a browser using its QR code or invitation link.

[简体中文说明](README.zh-CN.md)

## Why BendiSend

- **Runs on your computer.** Room data is stored on the host PC. Files and text are sent to and served by that host over your LAN; BendiSend does not upload them to a third-party cloud server.
- **Multiple separate rooms.** Each room has its own invitation, history, retention period, and participants.
- **Adjustable limits, no cloud plan caps.** The default is 10 GiB per file and 50 GiB shared across rooms. The host can change both. The practical maximum depends on free disk space, filesystem limits, browser support, and the configured room quota.
- **Designed for browser-based devices.** Join with a phone or computer browser; no account or client installation is needed on joining devices.
- **Resumable transfers.** Files are uploaded in chunks and can resume after a network interruption or host restart.
- **Chinese and English interface.** Choose a language in the top bar.

## Quick start

1. Build and run BendiSend on a Windows host connected to your trusted Wi-Fi.
2. Allow the app through Windows Firewall on a **Private network** if prompted.
3. Connect the other devices to the same Wi-Fi, then scan the room QR code or open its invitation link in a browser.
4. Send text and files from the composer. Files and text appear as separate cards in the received message.

On Windows, BendiSend opens Edge or Chrome in a standalone app window when available, then falls back to your default browser.

Keep the host app running while other devices use it. The default room is created automatically. On the host, use the room selector to create or switch rooms. Each new room has its own invitation link and QR code; share that room's invitation with participants.

## Storage and limits

- Records are kept for 7 days by default. The host can choose 1 day, 7 days, 30 days, or indefinite retention per room.
- The host can clear a room's published history or delete the room. Deleting the default room creates a new empty default room and revokes its old invitation.
- Default limits are 10 GiB per file, 50 GiB shared across rooms, 200 published messages per room, 20 files per message, and 16 KiB of text per message.
- The host can change file and shared storage limits in **Transfer limits**. In-progress uploads reserve space. The host disk must still have enough free space.
- Data is stored in `%LOCALAPPDATA%\LanTransfer` by default. This legacy path is retained so existing installations can keep using their data. Use `-data-dir` to choose another location.

## Network and security

BendiSend has no cloud relay or account service. The host serves the web app and transfer API directly over the local network using HTTP. **HTTP does not encrypt traffic**, and room invitations grant participants access to read, send, and delete content in that room. Files and records are stored unencrypted on the host.

Use it only on a trusted network. Do not expose the host port to the public internet or share an invitation with anyone who should not have room access. Remote access, TLS, and end-to-end encryption are not provided by this project.

## Build from source

Requires Go 1.23 or newer. The first build downloads the pinned QR-code dependency.

```powershell
go build -trimpath -o BendiSend.exe .
```

Run the host:

```powershell
.\BendiSend.exe
```

Optional flags:

- `-port 8766` selects a specific HTTP port. Without an explicit port, the app can choose another available port if the default is busy.
- `-data-dir D:\BendiSendData` selects the data directory.
- `-no-browser` starts the service without opening the host browser.

## License

BendiSend is released under the MIT License. Third-party notices are in [THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt).

