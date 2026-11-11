import 'package:flutter/material.dart';
import 'package:qr_code_scanner_plus/qr_code_scanner_plus.dart';

import '../app_state.dart';
import '../connect.dart';

/// Scans the pairing QR code shown by the web app's Devices page.
class ScanScreen extends StatefulWidget {
  const ScanScreen({super.key, required this.state});

  final AppState state;

  @override
  State<ScanScreen> createState() => _ScanScreenState();
}

class _ScanScreenState extends State<ScanScreen> {
  PairingLink? _link;
  bool _waiting = false;
  String? _error;

  final _viewKey = GlobalKey(debugLabel: 'qr');

  void _onViewCreated(QRViewController controller) {
    controller.scannedDataStream.listen((scan) => _onScan(scan.code ?? ''));
  }

  Future<void> _onScan(String text) async {
    if (_link != null) return;
    final link = PairingLink.parse(text);
    if (link == null) return;
    setState(() => _link = link);
    try {
      await pairWithCode(
        widget.state,
        link,
        onWaiting: () {
          if (mounted && !_waiting) setState(() => _waiting = true);
        },
      );
      if (mounted) Navigator.of(context).pop();
    } on Exception catch (e) {
      if (mounted) {
        setState(() {
          _error = '$e';
          _link = null;
          _waiting = false;
        });
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Scan pairing code')),
      body: _link == null
          ? Stack(
              children: [
                QRView(key: _viewKey, onQRViewCreated: _onViewCreated),
                if (_error != null)
                  Align(
                    alignment: Alignment.bottomCenter,
                    child: Container(
                      margin: const EdgeInsets.all(16),
                      padding: const EdgeInsets.all(16),
                      decoration: BoxDecoration(
                        color: theme.colorScheme.errorContainer,
                        borderRadius: BorderRadius.circular(12),
                      ),
                      child: Text(
                        _error!,
                        style: TextStyle(
                          color: theme.colorScheme.onErrorContainer,
                        ),
                      ),
                    ),
                  ),
              ],
            )
          : Center(
              child: Padding(
                padding: const EdgeInsets.all(32),
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const CircularProgressIndicator(),
                    const SizedBox(height: 24),
                    Text(
                      _waiting
                          ? 'Confirm this device in the web app'
                          : 'Connecting to ${Uri.parse(_link!.server).host}…',
                      style: theme.textTheme.titleMedium,
                      textAlign: TextAlign.center,
                    ),
                    const SizedBox(height: 8),
                    if (_waiting)
                      Text(
                        'The page showing the code now asks you to confirm.',
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                        textAlign: TextAlign.center,
                      ),
                  ],
                ),
              ),
            ),
    );
  }
}
