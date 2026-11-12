// The parts of the app that talk to the operating system: notifications,
// the desktop tray and window, and the Android background service.
import 'dart:io';

import 'package:flutter/widgets.dart';
import 'package:flutter_foreground_task/flutter_foreground_task.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';

import 'app_state.dart';
import 'background_service.dart';

final _notifications = FlutterLocalNotificationsPlugin();
var _nextNotification = 0;

/// Sets up system notifications and returns the function that shows one.
/// Never throws: without notifications the app still works.
Future<Notify> initNotifications({bool askPermission = true}) async {
  var ready = false;
  try {
    await _notifications.initialize(
      settings: const InitializationSettings(
        android: AndroidInitializationSettings('@mipmap/ic_launcher'),
        iOS: DarwinInitializationSettings(
          requestAlertPermission: false,
          requestBadgePermission: false,
          requestSoundPermission: false,
        ),
        macOS: DarwinInitializationSettings(
          requestAlertPermission: false,
          requestBadgePermission: false,
          requestSoundPermission: false,
        ),
        linux: LinuxInitializationSettings(defaultActionName: 'Open'),
        windows: WindowsInitializationSettings(
          appName: 'Codemail',
          appUserModelId: 'io.github.tikaydaes.codemail',
          guid: '6f1d6c0a-6b0c-4f0e-9a1f-2f7d3f6a9c41',
        ),
      ),
      // Tapping a notification asks the app to open that message.
      onDidReceiveNotificationResponse: (response) =>
          openMessageRequest.value = int.tryParse(response.payload ?? ''),
    );
    if (askPermission) {
      // The app may have been started by a tap on a notification.
      final launch = await _notifications.getNotificationAppLaunchDetails();
      if (launch?.didNotificationLaunchApp ?? false) {
        openMessageRequest.value = int.tryParse(
          launch?.notificationResponse?.payload ?? '',
        );
      }
    }
    ready = true;
  } on Object catch (e) {
    debugPrint('notifications unavailable: $e');
  }
  return (title, body, {messageId}) async {
    if (!ready) return;
    try {
      await _notifications.show(
        id: _nextNotification++,
        title: title,
        body: body,
        payload: messageId?.toString(),
        notificationDetails: const NotificationDetails(
          android: AndroidNotificationDetails(
            'codes',
            'Sign-in codes',
            channelDescription: 'New sign-in codes and confirmation links',
            importance: Importance.high,
            priority: Priority.high,
          ),
          iOS: DarwinNotificationDetails(),
          macOS: DarwinNotificationDetails(),
          linux: LinuxNotificationDetails(),
          windows: WindowsNotificationDetails(),
        ),
      );
    } on Object catch (e) {
      debugPrint('notification failed: $e');
    }
  };
}

/// Asks the user to allow notifications. Called once the device is
/// connected, when notifications start to matter, not at first launch.
Future<void> requestNotificationPermission() async {
  try {
    await _notifications
        .resolvePlatformSpecificImplementation<
          AndroidFlutterLocalNotificationsPlugin
        >()
        ?.requestNotificationsPermission();
    await _notifications
        .resolvePlatformSpecificImplementation<
          IOSFlutterLocalNotificationsPlugin
        >()
        ?.requestPermissions(alert: true, badge: true, sound: true);
    await _notifications
        .resolvePlatformSpecificImplementation<
          MacOSFlutterLocalNotificationsPlugin
        >()
        ?.requestPermissions(alert: true, badge: true, sound: true);
  } on Object catch (e) {
    debugPrint('notification permission request failed: $e');
  }
}

/// On desktop the app lives in the system tray: closing the window hides
/// it, and the live connection keeps running so codes still arrive.
class DesktopShell with TrayListener, WindowListener {
  Future<void> init() async {
    await windowManager.ensureInitialized();
    await windowManager.waitUntilReadyToShow(
      const WindowOptions(
        size: Size(440, 720),
        minimumSize: Size(360, 480),
        title: 'Codemail',
      ),
      () async {
        await windowManager.show();
        await windowManager.focus();
      },
    );
    try {
      // macOS menu bar icons are single-colour templates the system tints.
      if (Platform.isMacOS) {
        await trayManager.setIcon('assets/tray_template.png', isTemplate: true);
      } else {
        await trayManager.setIcon(
          Platform.isWindows ? 'assets/tray.ico' : 'assets/tray.png',
        );
      }
      await trayManager.setToolTip('Codemail');
      await trayManager.setContextMenu(
        Menu(
          items: [
            MenuItem(key: 'show', label: 'Open Codemail'),
            MenuItem.separator(),
            MenuItem(key: 'quit', label: 'Quit'),
          ],
        ),
      );
      trayManager.addListener(this);
      // Only hide on close once there is a tray icon to bring it back.
      await windowManager.setPreventClose(true);
      windowManager.addListener(this);
    } on Object catch (e) {
      debugPrint('tray unavailable: $e');
    }
  }

  /// Brings the window forward when a notification is tapped.
  void watchNotificationTaps() => openMessageRequest.addListener(() {
    if (openMessageRequest.value != null) _show();
  });

  Future<void> _show() async {
    await windowManager.show();
    await windowManager.focus();
  }

  @override
  void onWindowClose() => windowManager.hide();

  @override
  void onTrayIconMouseDown() => _show();

  @override
  void onTrayIconRightMouseDown() => trayManager.popUpContextMenu();

  @override
  void onTrayMenuItemClick(MenuItem menuItem) {
    if (menuItem.key == 'show') {
      _show();
    } else if (menuItem.key == 'quit') {
      windowManager.destroy();
    }
  }
}

/// Android stops background apps from holding a connection unless they
/// run a foreground service with a visible notification. No push service
/// is used, so this is how codes keep arriving when the app is not open.
/// The service runs [backgroundMain] in its own isolate, so it carries on
/// after the app is swiped away and starts again when the phone restarts.
Future<void> syncBackgroundService(AppState state) async {
  if (!Platform.isAndroid) return;
  try {
    final want = state.signedIn && state.stayConnected;
    final running = await FlutterForegroundTask.isRunningService;
    if (want && !running) {
      FlutterForegroundTask.init(
        androidNotificationOptions: AndroidNotificationOptions(
          channelId: 'connection',
          channelName: 'Connection',
          channelDescription:
              'Shown while Codemail stays connected to your server',
          channelImportance: NotificationChannelImportance.LOW,
          priority: NotificationPriority.LOW,
        ),
        iosNotificationOptions: const IOSNotificationOptions(),
        foregroundTaskOptions: ForegroundTaskOptions(
          eventAction: ForegroundTaskEventAction.repeat(60000),
          autoRunOnBoot: true,
          autoRunOnMyPackageReplaced: true,
          allowWakeLock: true,
          allowWifiLock: true,
        ),
      );
      await FlutterForegroundTask.startService(
        serviceTypes: [ForegroundServiceTypes.remoteMessaging],
        notificationTitle: 'Codemail is connected',
        notificationText: 'Sign-in codes arrive while this is on',
        callback: backgroundMain,
      );
    } else if (!want && running) {
      await FlutterForegroundTask.stopService();
    }
  } on Object catch (e) {
    debugPrint('background service unavailable: $e');
  }
}
