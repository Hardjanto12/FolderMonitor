FolderMonitor v1.3.6

Perubahan penting:
- Scan default sekarang memakai scanMode "date_folder".
- Aplikasi hanya memeriksa 3 folder tanggal terakhir berdasarkan pola yyyy\MMdd.
- Cocok untuk struktur seperti D:\Image\6201FS05\2026\0605\0971.
- CREATEDIR.NUL dan ekstensi .nul diabaikan.
- Alert dapat mengirim HTTP POST ke relay PC lewat LAN.
- HTTP POST berjalan async dengan timeout pendek agar tidak membuat GUI freeze.

Config utama untuk struktur X-ray:

  "folderPath": "D:\\Image\\6201FS05",
  "scanMode": "date_folder",
  "dateFolderPattern": "yyyy\\MMdd",
  "scanLookbackDays": 3,
  "includeSubfolders": true,
  "allowedExtensions": [".jpg", ".img"],
  "ignoreFileNames": ["CREATEDIR.NUL"]

Dengan tanggal 2026-06-05, aplikasi akan mengecek:
- D:\Image\6201FS05\2026\0605
- D:\Image\6201FS05\2026\0604
- D:\Image\6201FS05\2026\0603

Remote alert / relay PC:

  "remoteAlertEnabled": true,
  "remoteAlertUrl": "http://192.168.1.50:8765/api/alert",
  "remoteAlertSecret": "change_this_secret",
  "remoteAlertTimeoutSeconds": 3,
  "remoteAlertSourceName": "XRay-PC-01"

Header yang dikirim:
- Content-Type: application/json
- X-Relay-Secret: sesuai remoteAlertSecret

Contoh payload JSON:
{
  "source": "foldermonitor",
  "version": "1.3.6",
  "machineName": "XRAY-PC-01",
  "sourceName": "XRay-PC-01",
  "level": "ALERT",
  "alertName": "Folder Monitor Alert",
  "message": "ALERT: Tidak ada file baru/update selama 30 menit...",
  "folderPath": "D:\Image\6201FS05",
  "latestPath": "D:\Image\6201FS05\2026\0605\0971\file.jpg",
  "latestTime": "2026-06-05 18:09:00",
  "maxAgeMinutes": 30,
  "alertTime": "2026-06-05 18:40:00"
}

Test remote alert dari CMD:

  FolderMonitor.exe --test-remote-alert

Mode scan lama:
Jika ingin kembali scan folder langsung seperti versi lama:

  "scanMode": "all"

Build Windows x64 GUI:

  cd source
  GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o ..\FolderMonitor.exe .

Build console debug:

  cd source
  GOOS=windows GOARCH=amd64 go build -o ..\FolderMonitor_console_debug.exe .
