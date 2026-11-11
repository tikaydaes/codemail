import 'dart:convert';

import 'core/models.dart';

/// Where the device's tokens are kept: the platform keychain in the app,
/// memory in tests.
abstract class SecretStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
}

class MemorySecretStore implements SecretStore {
  final Map<String, String> values = {};

  @override
  Future<String?> read(String key) async => values[key];
  @override
  Future<void> write(String key, String value) async => values[key] = value;
  @override
  Future<void> delete(String key) async => values.remove(key);
}

/// The saved connection: which server, and this device's tokens. The app
/// and its Android background service both read and write it, which is how
/// each sees tokens the other has renewed.
class SessionStore {
  SessionStore(this.secrets);

  final SecretStore secrets;
  static const _key = 'session';

  Future<({String server, Tokens tokens})?> load() async {
    final raw = await secrets.read(_key);
    if (raw == null) return null;
    try {
      final j = jsonDecode(raw) as Map<String, dynamic>;
      return (
        server: j['server'] as String,
        tokens: Tokens(
          access: j['access'] as String,
          refresh: j['refresh'] as String,
          expiresAt: DateTime.fromMillisecondsSinceEpoch(j['expires'] as int),
        ),
      );
    } on Object {
      return null;
    }
  }

  Future<void> save(String server, Tokens t) => secrets.write(
    _key,
    jsonEncode({
      'server': server,
      'access': t.access,
      'refresh': t.refresh,
      'expires': t.expiresAt.millisecondsSinceEpoch,
    }),
  );

  Future<void> clear() => secrets.delete(_key);
}
