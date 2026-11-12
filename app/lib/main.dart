import 'dart:async';

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'app_state.dart';
import 'keychain_store.dart';
import 'platform_services.dart';
import 'ui/app.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  if (isDesktop) {
    try {
      final shell = DesktopShell();
      await shell.init();
      shell.watchNotificationTaps();
    } on Object catch (e) {
      debugPrint('desktop integration unavailable: $e');
    }
  }
  final state = AppState(
    secrets: KeychainStore(),
    prefs: await SharedPreferences.getInstance(),
  );
  state.addListener(() => syncBackgroundService(state));

  // Show the app first. Nothing below may hold up the first screen: the
  // notification set-up asks the user for permission, which can take as
  // long as they like (and on some systems never answers).
  runApp(CodemailApp(state: state));
  unawaited(state.load());
  unawaited(
    initNotifications().then((notify) {
      state.notify = notify;
      // Ask for permission the first time the device is connected.
      var asked = false;
      void askOnceConnected() {
        // (A build flag skips the prompt so automated runs are not blocked by it.)
        if (state.signedIn &&
            !asked &&
            !const bool.fromEnvironment('CODEMAIL_SKIP_NOTIFICATION_PROMPT')) {
          asked = true;
          requestNotificationPermission();
        }
      }

      state.addListener(askOnceConnected);
      askOnceConnected();
    }),
  );
}
