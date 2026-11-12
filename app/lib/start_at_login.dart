import 'dart:io';

/// Starts the app when the user signs in to their computer, using each
/// system's own mechanism: an autostart entry on Linux, a Run key in the
/// user's registry on Windows. Not available on macOS yet.
class StartAtLogin {
  StartAtLogin({
    String? executable,
    Map<String, String>? environment,
    bool? windows,
  }) : _exe = executable ?? Platform.resolvedExecutable,
       _env = environment ?? Platform.environment,
       _windows = windows ?? Platform.isWindows;

  final String _exe;
  final Map<String, String> _env;
  final bool _windows;

  static bool get supported => Platform.isLinux || Platform.isWindows;

  static const _runKey = r'HKCU\Software\Microsoft\Windows\CurrentVersion\Run';

  /// The Linux autostart entry, per the XDG autostart specification.
  File get desktopFile {
    final config = (_env['XDG_CONFIG_HOME'] ?? '').isNotEmpty
        ? _env['XDG_CONFIG_HOME']!
        : '${_env['HOME']}/.config';
    return File('$config/autostart/codemail.desktop');
  }

  Future<bool> isEnabled() async {
    if (_windows) {
      final r = await Process.run('reg', ['query', _runKey, '/v', 'Codemail']);
      return r.exitCode == 0;
    }
    return desktopFile.exists();
  }

  Future<void> setEnabled(bool enabled) async {
    if (_windows) {
      final args = enabled
          ? [
              'add',
              _runKey,
              '/v',
              'Codemail',
              '/t',
              'REG_SZ',
              '/d',
              '"$_exe"',
              '/f',
            ]
          : ['delete', _runKey, '/v', 'Codemail', '/f'];
      final r = await Process.run('reg', args);
      if (r.exitCode != 0 && enabled) {
        throw ProcessException('reg', args, '${r.stderr}', r.exitCode);
      }
      return;
    }
    final file = desktopFile;
    if (!enabled) {
      if (await file.exists()) await file.delete();
      return;
    }
    await file.parent.create(recursive: true);
    await file.writeAsString(
      '[Desktop Entry]\n'
      'Type=Application\n'
      'Name=Codemail\n'
      'Comment=Sign-in codes and links from your own server\n'
      'Exec="$_exe"\n'
      'Terminal=false\n'
      'X-GNOME-Autostart-enabled=true\n',
    );
  }
}
