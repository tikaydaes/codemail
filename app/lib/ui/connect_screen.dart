import 'dart:io' show Platform;

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../connect.dart';
import 'app.dart';
import 'scan_screen.dart';

class ConnectScreen extends StatefulWidget {
  const ConnectScreen({super.key, required this.state});

  final AppState state;

  @override
  State<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends State<ConnectScreen> {
  final _server = TextEditingController();
  bool _busy = false;

  @override
  void dispose() {
    _server.dispose();
    super.dispose();
  }

  Future<void> _signIn() async {
    setState(() => _busy = true);
    await attempt(context, () => signInWithBrowser(widget.state, _server.text));
    if (mounted) setState(() => _busy = false);
  }

  String get _deliveryNote {
    if (isDesktop) {
      return 'Codemail stays in your system tray and keeps a connection to your server, so codes arrive as soon as the email does.';
    }
    if (!isMobile) return '';
    return Platform.isAndroid
        ? 'This app talks only to your server and uses no Google push service. It stays connected in the background and shows a permanent "connected" notification.'
        : 'This app talks only to your server and uses no Apple push service, so codes appear while the app is open. Open it when a site says it has sent a code.';
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 440),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Text(
                    'Codemail',
                    style: theme.textTheme.titleMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(height: 4),
                  Text(
                    'Connect to your server',
                    style: theme.textTheme.headlineMedium,
                  ),
                  const SizedBox(height: 24),
                  if (widget.state.error != null) ...[
                    Text(
                      widget.state.error!,
                      style: TextStyle(color: theme.colorScheme.error),
                    ),
                    const SizedBox(height: 16),
                  ],
                  TextField(
                    controller: _server,
                    autocorrect: false,
                    keyboardType: TextInputType.url,
                    textInputAction: TextInputAction.go,
                    onSubmitted: (_) => _signIn(),
                    decoration: const InputDecoration(
                      labelText: 'Server address',
                      hintText: 'mail.example.com',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 16),
                  FilledButton(
                    onPressed: _busy ? null : _signIn,
                    child: Padding(
                      padding: const EdgeInsets.symmetric(vertical: 14),
                      child: Text(
                        _busy
                            ? 'Waiting for your browser…'
                            : 'Sign in with your browser',
                      ),
                    ),
                  ),
                  if (isMobile) ...[
                    const SizedBox(height: 12),
                    OutlinedButton(
                      onPressed: _busy
                          ? null
                          : () => Navigator.of(context).push(
                              MaterialPageRoute<void>(
                                builder: (_) => ScanScreen(state: widget.state),
                              ),
                            ),
                      child: const Padding(
                        padding: EdgeInsets.symmetric(vertical: 14),
                        child: Text('Scan a pairing code'),
                      ),
                    ),
                  ],
                  const SizedBox(height: 24),
                  Text(
                    'Your browser opens so you can sign in with your passkey and approve this device. '
                    '${isMobile ? 'Or open Devices in the web app, choose Add device, and scan the code it shows. ' : ''}'
                    '$_deliveryNote',
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
