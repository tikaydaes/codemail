import 'package:flutter/material.dart';

import '../app_state.dart';
import 'connect_screen.dart';
import 'home_screen.dart';

const seed = Color(0xFF1F4FD8);

/// Codes are set in a monospaced face so digits line up and are easy to read.
const codeStyle = TextStyle(
  fontFamily: 'monospace',
  fontFamilyFallback: ['Menlo', 'Consolas', 'DejaVu Sans Mono', 'Roboto Mono'],
  fontWeight: FontWeight.w600,
  letterSpacing: 1.5,
);

class CodemailApp extends StatelessWidget {
  const CodemailApp({super.key, required this.state});

  final AppState state;

  ThemeData _theme(Brightness brightness) => ThemeData(
    colorScheme: ColorScheme.fromSeed(seedColor: seed, brightness: brightness),
    useMaterial3: true,
    visualDensity: VisualDensity.standard,
  );

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Codemail',
      debugShowCheckedModeBanner: false,
      theme: _theme(Brightness.light),
      darkTheme: _theme(Brightness.dark),
      home: ListenableBuilder(
        listenable: state,
        builder: (context, _) {
          if (!state.loaded) {
            return const Scaffold(
              body: Center(child: CircularProgressIndicator()),
            );
          }
          return state.signedIn
              ? HomeScreen(state: state)
              : ConnectScreen(state: state);
        },
      ),
    );
  }
}

/// Runs an action and shows its failure in a snack bar instead of losing it.
Future<void> attempt(
  BuildContext context,
  Future<void> Function() action,
) async {
  final messenger = ScaffoldMessenger.of(context);
  try {
    await action();
  } on Exception catch (e) {
    messenger.showSnackBar(SnackBar(content: Text('$e')));
  }
}

void toast(BuildContext context, String text) {
  ScaffoldMessenger.of(context)
    ..hideCurrentSnackBar()
    ..showSnackBar(
      SnackBar(content: Text(text), duration: const Duration(seconds: 2)),
    );
}
