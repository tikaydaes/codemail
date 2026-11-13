# Codemail app

The Codemail client for Android, iOS, macOS, Windows and Linux, written
in Flutter. It connects to your own Codemail server and shows sign-in
codes and confirmation links as they arrive.

The server field starts out as `codegrab.me`, the public server run by the
Codemail developers; replace it with your own server's address if you have
one. Whoever runs the server can read the mail it receives, so use one you
trust: see "Whose server?" in the main README.

It is not published in the Apple App Store, Google Play or the Microsoft
Store. Builds are attached to the project's GitHub releases; the Android
build contains no Google libraries, so it is suitable for F-Droid.

## Build

    flutter pub get
    flutter test
    flutter build apk          # or: linux, windows, macos, ios --no-codesign

Android needs a JDK 17 and the Android SDK. iOS and macOS need Xcode.
Linux needs GTK 3, WebKitGTK 4.1, libsecret and Ayatana AppIndicator
development packages (see `.github/workflows/apps.yml` for the exact list).

## How it connects

Enter your server's address. The app opens your browser, where you sign in
with your passkey and approve the device; the server returns tokens to the
app (OAuth with PKCE). On phones you can instead scan the QR code shown
under Devices in the web app.

No push service is used. Desktop apps stay in the system tray. iPhones
receive codes while the app is open.

On Android a foreground service, with a permanent notification, holds the
connection in its own isolate (`lib/background_service.dart`). It keeps
running after the app is swiped away, starts again when the phone
restarts, and is the part that shows notifications for new codes. The app
and the service share one saved session; each checks whether the other
has already renewed the tokens before renewing them itself.

## Layout

- `lib/core` — API client, OAuth, live event stream. Plain Dart.
- `lib/app_state.dart` — session, messages, clipboard and preferences.
- `lib/core/watcher.dart` — the connection the background service runs.
- `lib/background_service.dart` — the Android service's entry point.
- `lib/platform_services.dart` — notifications, tray, starting the service.
- `lib/ui` — screens.
