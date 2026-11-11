// The Android background service. Android starts this in its own isolate,
// separate from the app's screens, and keeps it alive with a permanent
// notification. It holds the live connection to the server and announces
// new codes, so they arrive even when the app has been swiped away or the
// phone has restarted. No push service is involved.
import 'dart:ui';

import 'package:flutter/widgets.dart';
import 'package:flutter_foreground_task/flutter_foreground_task.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'core/api.dart';
import 'core/format.dart';
import 'core/watcher.dart';
import 'keychain_store.dart';
import 'platform_services.dart';
import 'session_store.dart';

/// Entry point Android calls to start the service's isolate.
@pragma('vm:entry-point')
void backgroundMain() {
  FlutterForegroundTask.setTaskHandler(_ConnectionHandler());
}

const _lastIdKey = 'backgroundLastId';

class _ConnectionHandler extends TaskHandler {
  CodeWatcher? _watcher;
  CodemailApi? _api;

  @override
  Future<void> onStart(DateTime timestamp, TaskStarter starter) async {
    WidgetsFlutterBinding.ensureInitialized();
    DartPluginRegistrant.ensureInitialized();
    final store = SessionStore(KeychainStore());
    final saved = await store.load();
    if (saved == null) {
      await FlutterForegroundTask.stopService();
      return;
    }
    final prefs = await SharedPreferences.getInstance();
    final notify = await initNotifications(askPermission: false);
    final api = CodemailApi(
      saved.server,
      tokens: saved.tokens,
      onTokens: (t) => store.save(saved.server, t),
      loadTokens: () async => (await store.load())?.tokens,
    );
    _api = api;
    _watcher = CodeWatcher(
      api: api,
      readLastId: () async => prefs.getInt(_lastIdKey),
      writeLastId: (id) => prefs.setInt(_lastIdKey, id),
      onSignedOut: FlutterForegroundTask.stopService,
      onCode: (m) async {
        // The user may have changed this in the app since the service started.
        await prefs.reload();
        final showCodes = prefs.getBool('showCodeInNotifications') ?? true;
        final body = !m.hasCode
            ? 'Confirmation link received'
            : showCodes
            ? showCode(m.code)
            : 'New sign-in code';
        await notify(m.title, body);
      },
    );
    await _watcher!.start();
  }

  // Runs once a minute: restarts the watcher if it has stopped.
  @override
  void onRepeatEvent(DateTime timestamp) {
    final w = _watcher;
    if (w != null && !w.running) w.start();
  }

  @override
  Future<void> onDestroy(DateTime timestamp, bool isTimeout) async {
    _watcher?.stop();
    _api?.close();
  }
}
