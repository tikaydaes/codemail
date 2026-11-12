import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../core/format.dart';
import '../core/models.dart';
import 'app.dart';
import 'home_screen.dart';

/// One message: what was found in it, who sent it, and its body. The body
/// arrives from the server as Markdown, with no images, scripts or styles.
class MessageScreen extends StatefulWidget {
  const MessageScreen({super.key, required this.state, required this.id});

  final AppState state;
  final int id;

  @override
  State<MessageScreen> createState() => _MessageScreenState();
}

class _MessageScreenState extends State<MessageScreen> {
  late Future<Message> _message = widget.state.api!.message(widget.id);

  void _reload() {
    // A block body: setState must not be handed the Future itself.
    setState(() {
      _message = widget.state.api!.message(widget.id);
    });
  }

  /// Body links open only after the reader has seen where they go.
  Future<void> _tapLink(String? href) async {
    final uri = Uri.tryParse(href ?? '');
    if (uri == null ||
        !const ['http', 'https', 'mailto', 'tel'].contains(uri.scheme)) {
      return;
    }
    final go = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Open this link?'),
        content: Text(uri.host.isEmpty ? uri.toString() : uri.host),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Open'),
          ),
        ],
      ),
    );
    if (go == true) await launchUrl(uri, mode: LaunchMode.externalApplication);
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final state = widget.state;
    return Scaffold(
      appBar: AppBar(title: const Text('Message')),
      body: FutureBuilder<Message>(
        future: _message,
        builder: (context, snap) {
          if (snap.hasError) {
            return Center(
              child: Padding(
                padding: const EdgeInsets.all(24),
                child: Text('${snap.error}'),
              ),
            );
          }
          final m = snap.data;
          if (m == null) {
            return const Center(child: CircularProgressIndicator());
          }
          final muted = theme.colorScheme.onSurfaceVariant;
          Widget fact(String label, String value) => Padding(
            padding: const EdgeInsets.symmetric(vertical: 3),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                SizedBox(
                  width: 104,
                  child: Text(label, style: TextStyle(color: muted)),
                ),
                Expanded(child: Text(value)),
              ],
            ),
          );
          Widget warning(String text, {bool severe = false}) => Container(
            margin: const EdgeInsets.only(bottom: 12),
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(
              color: severe
                  ? theme.colorScheme.errorContainer
                  : theme.colorScheme.tertiaryContainer,
              borderRadius: BorderRadius.circular(8),
            ),
            child: Text(
              text,
              style: TextStyle(
                color: severe
                    ? theme.colorScheme.onErrorContainer
                    : theme.colorScheme.onTertiaryContainer,
              ),
            ),
          );
          return ListView(
            padding: const EdgeInsets.all(16),
            children: [
              Text(
                m.subject.isEmpty ? '(no subject)' : m.subject,
                style: theme.textTheme.titleLarge,
              ),
              const SizedBox(height: 2),
              Text(m.from, style: TextStyle(color: muted)),
              const SizedBox(height: 16),
              if (m.unexpectedSender)
                warning(
                  'This address was generated for another sender. It may have been leaked or sold.',
                ),
              if (m.auth == 'fail')
                warning(
                  'The sender could not be verified. Treat this message with suspicion.',
                  severe: true,
                ),
              if (m.hasCode)
                Padding(
                  padding: const EdgeInsets.only(bottom: 12),
                  child: SelectableText(
                    showCode(m.code),
                    textAlign: TextAlign.center,
                    style: codeStyle.copyWith(fontSize: 36),
                  ),
                ),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  if (m.hasCode)
                    FilledButton(
                      onPressed: () async {
                        await state.useCode(m);
                        if (context.mounted) toast(context, 'Code copied');
                        _reload();
                      },
                      child: const Text('Copy code'),
                    ),
                  if (m.hasLink) ...[
                    FilledButton(
                      onPressed: () async {
                        await openLink(context, state, m);
                        _reload();
                      },
                      child: const Text('Open link'),
                    ),
                    OutlinedButton(
                      onPressed: () async {
                        await Clipboard.setData(ClipboardData(text: m.link));
                        if (context.mounted) toast(context, 'Link copied');
                      },
                      child: const Text('Copy link'),
                    ),
                  ],
                  if (m.used)
                    OutlinedButton(
                      onPressed: () async {
                        await state.setUsed(m, false);
                        _reload();
                      },
                      child: const Text('Mark unused'),
                    ),
                  OutlinedButton(
                    onPressed: () async {
                      await showCorrectionDialog(context, state, m);
                      _reload();
                    },
                    child: Text(
                      m.hasCode || m.hasLink ? 'Wrong code?' : 'Missed a code?',
                    ),
                  ),
                  OutlinedButton(
                    style: OutlinedButton.styleFrom(
                      foregroundColor: theme.colorScheme.error,
                    ),
                    onPressed: () async {
                      final navigator = Navigator.of(context);
                      await state.delete(m);
                      navigator.pop();
                    },
                    child: const Text('Delete'),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              fact('To', m.rcpt),
              fact(
                'Received',
                m.receivedAt == null
                    ? ''
                    : '${m.receivedAt!.toLocal()}'.split('.').first,
              ),
              fact('Sender check', switch (m.auth) {
                'pass' => 'Verified',
                'fail' => 'Failed',
                _ => 'Not checked',
              }),
              if (m.hasLink)
                fact(
                  'Link',
                  m.linkMatchesSender
                      ? 'Goes to the sender\'s own domain (${m.linkHost})'
                      : 'Goes to ${m.linkHost}, NOT the sender\'s domain',
                ),
              if (m.expiresAt != null)
                fact(
                  'Deleted on',
                  '${m.expiresAt!.toLocal()}'.split(' ').first,
                ),
              const Divider(height: 32),
              if ((m.markdown ?? '').isEmpty)
                Text('(empty message)', style: TextStyle(color: muted))
              else
                MarkdownBody(
                  data: m.markdown!,
                  selectable: true,
                  onTapLink: (text, href, title) => _tapLink(href),
                  // Messages never contain images, but if one slipped
                  // through it must not be fetched.
                  imageBuilder: (uri, title, alt) => Text(alt ?? ''),
                ),
            ],
          );
        },
      ),
    );
  }
}

/// Lets the user say what a message's code and link really are, choosing
/// from everything in the message that could be one. Corrections are
/// saved on their server and can be used to improve extraction.
Future<void> showCorrectionDialog(
  BuildContext context,
  AppState state,
  Message m,
) async {
  final List<Candidate> candidates;
  try {
    candidates = await state.api!.candidates(m.id);
  } on Exception catch (e) {
    if (context.mounted) toast(context, '$e');
    return;
  }
  if (!context.mounted) return;
  var code = m.code;
  var link = m.link;
  final save = await showDialog<bool>(
    context: context,
    builder: (context) => StatefulBuilder(
      builder: (context, setState) {
        Widget option(
          String label,
          bool selected,
          VoidCallback onTap, {
          bool mono = false,
          String note = '',
        }) => ListTile(
          dense: true,
          contentPadding: EdgeInsets.zero,
          leading: Icon(
            selected
                ? Icons.radio_button_checked
                : Icons.radio_button_unchecked,
          ),
          title: Text(
            label,
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: mono
                ? codeStyle.copyWith(fontSize: 15, letterSpacing: 0.5)
                : null,
          ),
          subtitle: note.isEmpty
              ? null
              : Text(note, maxLines: 1, overflow: TextOverflow.ellipsis),
          selected: selected,
          onTap: onTap,
        );
        return AlertDialog(
          title: const Text('Correct this message'),
          content: SizedBox(
            width: 420,
            child: ListView(
              shrinkWrap: true,
              children: [
                Text(
                  'Which is the sign-in code?',
                  style: Theme.of(context).textTheme.labelLarge,
                ),
                for (final c in candidates.where((c) => c.kind == 'code'))
                  option(
                    showCode(c.value),
                    code == c.value,
                    () => setState(() => code = c.value),
                    mono: true,
                    note: c.context,
                  ),
                option(
                  'There is no sign-in code',
                  code.isEmpty,
                  () => setState(() => code = ''),
                ),
                const SizedBox(height: 12),
                Text(
                  'Which is the confirmation link?',
                  style: Theme.of(context).textTheme.labelLarge,
                ),
                for (final c in candidates.where((c) => c.kind == 'link'))
                  option(
                    Uri.tryParse(c.value)?.host ?? c.value,
                    link == c.value,
                    () => setState(() => link = c.value),
                    note: c.context.isEmpty ? c.value : c.context,
                  ),
                option(
                  'There is no confirmation link',
                  link.isEmpty,
                  () => setState(() => link = ''),
                ),
              ],
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context, false),
              child: const Text('Cancel'),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(context, true),
              child: const Text('Save'),
            ),
          ],
        );
      },
    ),
  );
  if (save != true) return;
  await state.correct(m, code: code, link: link);
  if (context.mounted) {
    toast(context, 'Thanks. The correction is saved on your server.');
  }
}
