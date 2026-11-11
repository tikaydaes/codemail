import 'dart:io';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:path_provider/path_provider.dart';

import 'session_store.dart';

/// Keeps the device's tokens in the platform keychain. Where no keychain
/// is usable (an unsigned macOS build, or a Linux desktop without a
/// secret service) it falls back to a file only this user can read, so
/// the app still stays signed in.
class KeychainStore implements SecretStore {
  final _storage = const FlutterSecureStorage();
  bool _useFile = false;

  Future<File> _file(String key) async {
    final dir = await getApplicationSupportDirectory();
    return File('${dir.path}/$key.secret');
  }

  @override
  Future<String?> read(String key) async {
    if (!_useFile) {
      try {
        final v = await _storage.read(key: key);
        if (v != null) return v;
      } on Object {
        _useFile = true;
      }
    }
    final f = await _file(key);
    return f.existsSync() ? f.readAsString() : null;
  }

  @override
  Future<void> write(String key, String value) async {
    if (!_useFile) {
      try {
        return await _storage.write(key: key, value: value);
      } on Object {
        _useFile = true;
      }
    }
    final f = await _file(key);
    await f.writeAsString(value, flush: true);
    if (!Platform.isWindows) await Process.run('chmod', ['600', f.path]);
  }

  @override
  Future<void> delete(String key) async {
    try {
      await _storage.delete(key: key);
    } on Object {
      // Nothing was stored there.
    }
    final f = await _file(key);
    if (f.existsSync()) await f.delete();
  }
}
