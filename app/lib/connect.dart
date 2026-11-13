import 'dart:async';
import 'dart:io';

import 'package:flutter_web_auth_2/flutter_web_auth_2.dart';
import 'package:url_launcher/url_launcher.dart';

import 'app_state.dart';
import 'core/api.dart';
import 'core/format.dart';

String get _deviceName {
  try {
    return Platform.localHostname;
  } on Object {
    return 'Codemail app';
  }
}

/// The public server offered when the user has not named their own.
const defaultServer = 'codegrab.me';

/// Signs this device in with OAuth: the user approves it in their browser,
/// where they sign in with a passkey, and the browser hands a one-time
/// code back to the app.
Future<void> signInWithBrowser(AppState state, String serverInput) async {
  final server = normalizeServer(serverInput);
  if (server == null) {
    throw ApiException(
      'Enter your server\'s address, such as mail.example.com.',
    );
  }
  final api = state.newApi(server);
  await api.probe();
  final pkce = Pkce.generate();

  final Uri result;
  final String redirect;
  if (isDesktop) {
    // Desktop: the browser returns to a one-off listener on this machine.
    final listener = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    redirect = 'http://127.0.0.1:${listener.port}/callback';
    try {
      final url = api.authorizeUrl(
        pkce: pkce,
        redirectUri: redirect,
        deviceName: _deviceName,
        platform: Platform.operatingSystem,
      );
      if (!await launchUrl(url, mode: LaunchMode.externalApplication)) {
        throw ApiException('Could not open your browser.');
      }
      final request = await listener.first.timeout(
        const Duration(minutes: 5),
        onTimeout: () =>
            throw ApiException('Sign-in was not completed in time.'),
      );
      result = request.uri;
      request.response
        ..headers.contentType = ContentType.html
        ..write(
          '<!doctype html><meta charset="utf-8"><title>Codemail</title>'
          '<body style="font-family:system-ui;padding:48px;text-align:center">'
          '<h1>You can close this tab</h1><p>Return to the Codemail app.</p>',
        );
      await request.response.close();
    } finally {
      await listener.close(force: true);
    }
  } else {
    // Mobile: the system's sign-in sheet returns through the app's own
    // codemail:// address.
    redirect = 'codemail://oauth';
    final url = api.authorizeUrl(
      pkce: pkce,
      redirectUri: redirect,
      deviceName: _deviceName,
      platform: Platform.operatingSystem,
    );
    final String callback;
    try {
      callback = await FlutterWebAuth2.authenticate(
        url: url.toString(),
        callbackUrlScheme: 'codemail',
      );
    } on Exception {
      throw ApiException('Sign-in was cancelled.');
    }
    result = Uri.parse(callback);
  }

  final q = result.queryParameters;
  if (q['error'] != null) {
    throw ApiException('Access was denied in the browser.');
  }
  if (q['state'] != pkce.state || (q['code'] ?? '').isEmpty) {
    throw ApiException('The browser returned an unexpected reply. Try again.');
  }
  await api.exchangeCode(code: q['code']!, pkce: pkce, redirectUri: redirect);
  await state.connected(api);
}

/// A scanned pairing QR code: codemail://pair?server=...&code=...
class PairingLink {
  PairingLink._(this.server, this.code);

  static PairingLink? parse(String text) {
    final u = Uri.tryParse(text.trim());
    if (u == null || u.scheme != 'codemail' || u.host != 'pair') return null;
    final server = normalizeServer(u.queryParameters['server'] ?? '');
    final code = u.queryParameters['code'] ?? '';
    return server == null || code.isEmpty ? null : PairingLink._(server, code);
  }

  final String server;
  final String code;
}

/// Pairs with a scanned code, waiting for the user to confirm this device
/// in the web app. [onWaiting] is called once the server has the request.
Future<void> pairWithCode(
  AppState state,
  PairingLink link, {
  void Function()? onWaiting,
}) async {
  final api = state.newApi(link.server);
  final deadline = DateTime.now().add(const Duration(minutes: 2));
  while (true) {
    try {
      await api.pair(
        code: link.code,
        deviceName: _deviceName,
        platform: Platform.operatingSystem,
      );
      break;
    } on PairingPendingException {
      onWaiting?.call();
      if (DateTime.now().isAfter(deadline)) {
        throw ApiException(
          'The pairing code expired. Show a new one and scan again.',
        );
      }
      await Future<void>.delayed(const Duration(seconds: 2));
    }
  }
  await state.connected(api);
}
