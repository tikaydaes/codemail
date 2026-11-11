import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'app_state.dart';
import 'keychain_store.dart';
import 'platform_services.dart';
import 'ui/app.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  if (isDesktop) await DesktopShell().init();
  final state = AppState(
    secrets: KeychainStore(),
    prefs: await SharedPreferences.getInstance(),
    notify: await initNotifications(),
  );
  state.addListener(() => syncBackgroundService(state));
  runApp(CodemailApp(state: state));
  await state.load();
}
