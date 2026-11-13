import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:codemail/app_state.dart';
import 'package:codemail/core/models.dart';
import 'package:codemail/ui/app.dart';
import 'package:codemail/ui/message_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

int _now() => DateTime.now().millisecondsSinceEpoch ~/ 1000;

List<Map<String, Object>> _messages() => [
  {
    'id': 9,
    'kind': 'code',
    'service': 'Northwind Bank',
    'subject': 'Your one-time passcode',
    'rcpt': 'kaity+bank@codes.example',
    'code': '834201',
    'auth': 'pass',
    'flags': <String>[],
    'received_at': _now() - 12,
  },
  {
    'id': 8,
    'kind': 'code',
    'service': 'Acme Cloud',
    'subject': 'Confirm your email address',
    'rcpt': 'cloud@kaity.example',
    'link': 'https://app.acmecloud.example/verify?t=1',
    'link_host': 'app.acmecloud.example',
    'link_matches_sender': true,
    'auth': 'pass',
    'flags': <String>[],
    'received_at': _now() - 3600,
  },
  {
    'id': 7,
    'kind': 'code',
    'service': 'Parcelly',
    'subject': '482913 is your Parcelly code',
    'rcpt': 'shop@codes.example',
    'code': '482913',
    'auth': 'none',
    'flags': <String>['unexpected_sender'],
    'received_at': _now() - 7200,
    'used_at': _now() - 7000,
  },
];

const _markdown =
    '# Confirm your email\n\nHi **Kaity**,\n\nConfirm your email address to finish creating your account.\n\n'
    '[Verify email](https://app.acmecloud.example/verify?t=1)\n\n- This link expires in 24 hours\n- Ignore this if it was not you';

/// Answers the API calls the screens make.
http.Client fakeClient({List<String>? calls}) => MockClient.streaming((
  request,
  body,
) async {
  calls?.add('${request.method} ${request.url.path}');
  http.StreamedResponse json(Object data, [int status = 200]) =>
      http.StreamedResponse(
        Stream.value(utf8.encode(jsonEncode(data))),
        status,
        headers: {'content-type': 'application/json'},
      );
  final path = request.url.path;
  if (path == '/api/v1/me') {
    return json({
      'user': {'username': 'kaity'},
    });
  }
  if (path == '/api/v1/events') {
    return http.StreamedResponse(
      Stream.value(utf8.encode(': connected\n\n')),
      200,
    );
  }
  if (path == '/api/v1/messages') {
    final q = (request.url.queryParameters['q'] ?? '').toLowerCase();
    final all = request.url.queryParameters['kind'] == 'code'
        ? _messages()
        : <Map<String, Object>>[];
    return json({
      'messages': [
        for (final m in all)
          if (q.isEmpty || '${m['service']}'.toLowerCase().contains(q)) m,
      ],
    });
  }
  if (path == '/api/v1/messages/7/candidates') {
    return json({
      'candidates': [
        {
          'kind': 'code',
          'value': '482913',
          'context': '482913 is your Parcelly code',
        },
        {'kind': 'code', 'value': '2026', 'context': 'Copyright 2026 Parcelly'},
      ],
    });
  }
  if (path == '/api/v1/messages/7/correction') {
    return json({
      'message': {..._messages()[2], 'code': '', 'kind': 'other'},
    });
  }
  if (path == '/api/v1/messages/7') {
    return json({
      'message': {
        ..._messages()[2],
        'markdown': 'Your code is 482913',
        'from': 'Parcelly <no-reply@parcelly.example>',
      },
    });
  }
  if (path == '/api/v1/messages/8') {
    return json({
      'message': {
        ..._messages()[1],
        'markdown': _markdown,
        'from': 'Acme Cloud <hello@acmecloud.example>',
        'expires_at': _now() + 600000,
      },
    });
  }
  if (path == '/api/v1/messages/9' && request.method == 'PATCH') {
    return json({
      'message': {..._messages()[0], 'used_at': _now()},
    });
  }
  if (path == '/api/v1/addresses') {
    return json({
      'username': 'kaity',
      'shared_domains': [
        {
          'id': 1,
          'name': 'codes.example',
          'allow_aliases': true,
          'allow_subdomains': true,
        },
      ],
      'domains': [
        {'id': 2, 'name': 'kaity.example', 'verified': true},
      ],
      'aliases': [
        {
          'id': 3,
          'address': 'quiet-fern-4821@codes.example',
          'label': 'Northwind Bank',
          'generated': true,
          'expected_sender': 'northwind.example',
        },
        {
          'id': 4,
          'address': 'kaity@codes.example',
          'label': '',
          'generated': false,
        },
      ],
    });
  }
  return json({'error': 'not found'}, 404);
});

Future<AppState> signedInState({List<String>? calls}) async {
  SharedPreferences.setMockInitialValues({});
  final secrets = MemorySecretStore();
  await secrets.write(
    'session',
    jsonEncode({
      'server': 'https://mail.codes.example',
      'access': 'a',
      'refresh': 'r',
      'expires': DateTime.now()
          .add(const Duration(hours: 1))
          .millisecondsSinceEpoch,
    }),
  );
  return AppState(
    secrets: secrets,
    prefs: await SharedPreferences.getInstance(),
    clientFactory: () => fakeClient(calls: calls),
  );
}

/// Real fonts, so the saved screenshots are readable.
Future<void> loadFonts() async {
  final dir =
      '${Platform.environment['FLUTTER_ROOT'] ?? ''}/bin/cache/artifacts/material_fonts';
  Future<void> load(String family, List<String> files) async {
    final loader = FontLoader(family);
    for (final f in files) {
      final file = File('$dir/$f');
      if (!file.existsSync()) continue;
      loader.addFont(file.readAsBytes().then((b) => ByteData.view(b.buffer)));
    }
    await loader.load();
  }

  await load('Roboto', [
    'Roboto-Regular.ttf',
    'Roboto-Medium.ttf',
    'Roboto-Bold.ttf',
  ]);
  await load('MaterialIcons', ['MaterialIcons-Regular.otf']);
  await load('monospace', ['Roboto-Medium.ttf']);
}

/// Saves what is on screen to build/screens, for a human to look at.
Future<void> screenshot(WidgetTester tester, String name) async {
  final boundary = tester.renderObject<RenderRepaintBoundary>(
    find.byKey(const ValueKey('shot')),
  );
  await tester.runAsync(() async {
    final image = await boundary.toImage(pixelRatio: 2);
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    final file = File('build/screens/$name.png')..createSync(recursive: true);
    file.writeAsBytesSync(bytes!.buffer.asUint8List());
  });
}

Widget shot(Widget child) =>
    RepaintBoundary(key: const ValueKey('shot'), child: child);

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUpAll(loadFonts);

  // Clipboard access in tests goes through this stand-in.
  String? clipboard;
  setUp(() {
    clipboard = null;
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(SystemChannels.platform, (call) async {
          if (call.method == 'Clipboard.setData') {
            clipboard = (call.arguments as Map)['text'] as String?;
          }
          if (call.method == 'Clipboard.getData') return {'text': clipboard};
          return null;
        });
  });

  Future<AppState> pump(
    WidgetTester tester,
    Size size, {
    ThemeMode mode = ThemeMode.light,
    List<String>? calls,
  }) async {
    tester.view.physicalSize = size * 2;
    tester.view.devicePixelRatio = 2;
    addTearDown(tester.view.reset);
    late AppState state;
    await tester.runAsync(() async {
      state = await signedInState(calls: calls);
      await state.load();
    });
    addTearDown(state.dispose);
    await tester.pumpWidget(shot(CodemailApp(state: state)));
    await tester.pump();
    return state;
  }

  testWidgets('shows the newest code and copies it', (tester) async {
    final calls = <String>[];
    final state = await pump(tester, const Size(390, 844), calls: calls);
    expect(state.username, 'kaity');
    expect(
      find.text('834 201'),
      findsOneWidget,
      reason: 'the fresh code is shown grouped',
    );
    expect(find.text('Northwind Bank'), findsOneWidget);
    expect(find.text('Open link'), findsOneWidget);
    expect(find.text('482 913'), findsOneWidget);
    expect(find.textContaining('unexpected sender'), findsOneWidget);
    await screenshot(tester, 'phone-codes');

    await tester.runAsync(() async {
      await tester.tap(find.text('Copy code'));
      await Future<void>.delayed(const Duration(milliseconds: 100));
    });
    await tester.pump();
    expect(clipboard, '834201', reason: 'copied without the space');
    expect(calls, contains('PATCH /api/v1/messages/9'));
    expect(state.codes.firstWhere((m) => m.id == 9).used, isTrue);
    await tester.pump(
      const Duration(seconds: 3),
    ); // let the snack bar and timers finish
  });

  testWidgets('renders a message body from Markdown', (tester) async {
    final state = await pump(tester, const Size(390, 844));
    late Message m;
    await tester.runAsync(() async => m = await state.api!.message(8));
    expect(m.markdown, contains('Verify email'));
    await tester.pumpWidget(
      shot(
        MaterialApp(
          theme: ThemeData(colorScheme: ColorScheme.fromSeed(seedColor: seed)),
          home: MessageScreen(state: state, id: 8),
        ),
      ),
    );
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 100)),
    );
    await tester.pumpAndSettle();
    expect(find.text('Confirm your email address'), findsOneWidget);
    expect(find.text('Open link'), findsOneWidget);
    expect(
      find.textContaining('Goes to the sender\'s own domain'),
      findsOneWidget,
    );
    expect(find.byType(Image), findsNothing);
    await screenshot(tester, 'phone-message');
  });

  testWidgets('desktop width uses a side rail; addresses and settings render', (
    tester,
  ) async {
    await pump(tester, const Size(900, 640));
    expect(find.byType(NavigationRail), findsOneWidget);
    await screenshot(tester, 'desktop-codes');

    await tester.tap(find.text('Addresses'));
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 100)),
    );
    await tester.pumpAndSettle();
    expect(find.text('quiet-fern-4821@codes.example'), findsOneWidget);
    expect(find.text('kaity+anything@codes.example'), findsOneWidget);
    expect(find.text('anything@kaity.example'), findsOneWidget);
    await screenshot(tester, 'desktop-addresses');

    await tester.tap(find.text('Settings'));
    await tester.pumpAndSettle();
    expect(find.text('Signed in as kaity'), findsOneWidget);
    expect(find.text('Disconnect this device'), findsOneWidget);
    await screenshot(tester, 'desktop-settings');
  });

  testWidgets('search narrows the list', (tester) async {
    final calls = <String>[];
    await pump(tester, const Size(390, 844), calls: calls);
    await tester.enterText(find.byType(TextField), 'parc');
    await tester.pump(const Duration(milliseconds: 350)); // the debounce
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 100)),
    );
    await tester.pump();
    expect(find.text('Parcelly'), findsOneWidget);
    expect(find.text('Northwind Bank'), findsNothing);
    expect(
      find.text('Copy code'),
      findsNothing,
      reason: 'no featured code while searching',
    );
    await screenshot(tester, 'phone-search');
    await tester.tap(find.byTooltip('Clear search'));
    await tester.pump();
    expect(find.text('Northwind Bank'), findsOneWidget);
  });

  testWidgets(
    'a tapped notification opens its message, which can be corrected',
    (tester) async {
      final calls = <String>[];
      final state = await pump(tester, const Size(390, 844), calls: calls);
      openMessageRequest.value = 7; // what tapping a notification does
      await tester.pump();
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 100)),
      );
      await tester.pumpAndSettle();
      expect(find.text('482913 is your Parcelly code'), findsOneWidget);
      expect(openMessageRequest.value, isNull);

      await tester.runAsync(() async {
        await tester.tap(find.text('Wrong code?'));
        await Future<void>.delayed(const Duration(milliseconds: 100));
      });
      await tester.pumpAndSettle();
      expect(find.text('Correct this message'), findsOneWidget);
      expect(
        find.text('2026'),
        findsOneWidget,
        reason: 'other candidates are offered',
      );
      await screenshot(tester, 'phone-correction');
      await tester.tap(find.text('There is no sign-in code'));
      await tester.pump();
      await tester.runAsync(() async {
        await tester.tap(find.text('Save'));
        await Future<void>.delayed(const Duration(milliseconds: 150));
      });
      await tester.pump(const Duration(seconds: 3));
      expect(calls, contains('POST /api/v1/messages/7/correction'));
      expect(
        state.codes.any((m) => m.id == 7),
        isFalse,
        reason: 'it no longer counts as a code',
      );
      expect(state.other.any((m) => m.id == 7), isTrue);
    },
  );

  testWidgets('shows the connect screen when not signed in', (tester) async {
    SharedPreferences.setMockInitialValues({});
    final state = AppState(
      secrets: MemorySecretStore(),
      prefs: await SharedPreferences.getInstance(),
    );
    addTearDown(state.dispose);
    await state.load();
    tester.view.physicalSize = const Size(780, 1688);
    tester.view.devicePixelRatio = 2;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(shot(CodemailApp(state: state)));
    await tester.pump();
    expect(find.text('Connect to your server'), findsOneWidget);
    expect(find.text('Sign in with your browser'), findsOneWidget);
    expect(find.widgetWithText(TextField, 'codegrab.me'), findsOneWidget);
    expect(
      find.textContaining('can read the mail it receives'),
      findsOneWidget,
    );
    await screenshot(tester, 'phone-connect');
  });
}
