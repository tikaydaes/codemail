import 'dart:async';
import 'dart:io' show Platform;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../core/format.dart';
import '../core/models.dart';
import 'app.dart';
import 'message_screen.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.state});

  final AppState state;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  int _index = 0;

  static const _destinations = [
    (icon: Icons.password, label: 'Codes'),
    (icon: Icons.mail_outline, label: 'Other mail'),
    (icon: Icons.alternate_email, label: 'Addresses'),
    (icon: Icons.settings_outlined, label: 'Settings'),
  ];

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    final theme = Theme.of(context);
    final page = switch (_index) {
      0 => MessagesPage(state: state, kind: 'code'),
      1 => MessagesPage(state: state, kind: 'other'),
      2 => AddressesPage(state: state),
      _ => SettingsPage(state: state),
    };
    final wide = MediaQuery.sizeOf(context).width >= 720;
    final body = Column(
      children: [
        if (state.error != null)
          Material(
            color: theme.colorScheme.errorContainer,
            child: ListTile(
              dense: true,
              title: Text(
                state.error!,
                style: TextStyle(color: theme.colorScheme.onErrorContainer),
              ),
              trailing: TextButton(
                onPressed: state.refresh,
                child: const Text('Retry'),
              ),
            ),
          ),
        Expanded(child: page),
      ],
    );
    return Scaffold(
      appBar: AppBar(
        title: Text(_destinations[_index].label),
        actions: [
          Padding(
            padding: const EdgeInsets.only(right: 16),
            child: LiveChip(live: state.live),
          ),
        ],
      ),
      body: wide
          ? Row(
              children: [
                NavigationRail(
                  selectedIndex: _index,
                  onDestinationSelected: (i) => setState(() => _index = i),
                  labelType: NavigationRailLabelType.all,
                  destinations: [
                    for (final d in _destinations)
                      NavigationRailDestination(
                        icon: Icon(d.icon),
                        label: Text(d.label),
                      ),
                  ],
                ),
                const VerticalDivider(width: 1),
                Expanded(child: body),
              ],
            )
          : body,
      bottomNavigationBar: wide
          ? null
          : NavigationBar(
              selectedIndex: _index,
              onDestinationSelected: (i) => setState(() => _index = i),
              destinations: [
                for (final d in _destinations)
                  NavigationDestination(icon: Icon(d.icon), label: d.label),
              ],
            ),
    );
  }
}

class LiveChip extends StatelessWidget {
  const LiveChip({super.key, required this.live});

  final bool live;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final color = live ? const Color(0xFF0F6B3E) : scheme.onSurfaceVariant;
    return Semantics(
      label: live ? 'Connected live' : 'Reconnecting',
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            Icons.circle,
            size: 10,
            color: live ? const Color(0xFF1E9E5E) : scheme.outline,
          ),
          const SizedBox(width: 6),
          Text(
            live ? 'Live' : 'Reconnecting',
            style: TextStyle(
              color: Theme.of(context).brightness == Brightness.dark && live
                  ? const Color(0xFF6FD3A0)
                  : color,
              fontWeight: FontWeight.w600,
            ),
          ),
        ],
      ),
    );
  }
}

/// Opens a confirmation link, asking first when it does not go to the
/// sender's own domain.
Future<void> openLink(BuildContext context, AppState state, Message m) async {
  if (!m.linkMatchesSender) {
    final go = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Check this link'),
        content: Text(
          'This link goes to ${m.linkHost}, which is not the sender\'s domain '
          '(${m.senderDomain.isEmpty ? 'unknown' : m.senderDomain}). Open it anyway?',
        ),
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
    if (go != true) return;
  }
  await launchUrl(Uri.parse(m.link), mode: LaunchMode.externalApplication);
  await state.setUsed(m, true);
}

class MessagesPage extends StatefulWidget {
  const MessagesPage({super.key, required this.state, required this.kind});

  final AppState state;
  final String kind;

  @override
  State<MessagesPage> createState() => _MessagesPageState();
}

class _MessagesPageState extends State<MessagesPage> {
  late final Timer _tick;

  @override
  void initState() {
    super.initState();
    // Keeps "12 s ago" and the ten-minute freshness current.
    _tick = Timer.periodic(const Duration(seconds: 15), (_) => setState(() {}));
  }

  @override
  void dispose() {
    _tick.cancel();
    super.dispose();
  }

  Future<void> _copy(Message m) async {
    await widget.state.useCode(m);
    if (mounted) toast(context, 'Code copied');
  }

  void _open(Message m) => Navigator.of(context).push(
    MaterialPageRoute<void>(
      builder: (_) => MessageScreen(state: widget.state, id: m.id),
    ),
  );

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    final theme = Theme.of(context);
    final isCodes = widget.kind == 'code';
    final messages = isCodes ? state.codes : state.other;
    final now = DateTime.now();
    final fresh = isCodes
        ? messages.where((m) => m.isFresh(now)).firstOrNull
        : null;
    final rest = [
      for (final m in messages)
        if (m != fresh) m,
    ];

    return RefreshIndicator(
      onRefresh: state.refresh,
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          if (fresh != null)
            _FreshCard(
              message: fresh,
              onCopy: () => _copy(fresh),
              onOpen: () => _open(fresh),
            ),
          if (messages.isEmpty)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 64, horizontal: 24),
              child: Text(
                isCodes
                    ? 'No codes yet. Mail sent to your addresses appears here the moment it arrives.'
                    : 'Mail without a code or link appears here.',
                textAlign: TextAlign.center,
                style: theme.textTheme.bodyLarge?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
          if (rest.isNotEmpty) ...[
            if (fresh != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(4, 20, 4, 8),
                child: Text(
                  'EARLIER',
                  style: theme.textTheme.labelMedium?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                    letterSpacing: 0.6,
                  ),
                ),
              ),
            Card(
              margin: EdgeInsets.zero,
              clipBehavior: Clip.antiAlias,
              child: Column(
                children: [
                  for (final (i, m) in rest.indexed) ...[
                    if (i > 0) const Divider(height: 1),
                    _MessageTile(
                      message: m,
                      now: now,
                      onTap: () => _open(m),
                      onCopy: () => _copy(m),
                      onLink: () => openLink(context, state, m),
                    ),
                  ],
                ],
              ),
            ),
          ],
        ],
      ),
    );
  }
}

class _FreshCard extends StatelessWidget {
  const _FreshCard({
    required this.message,
    required this.onCopy,
    required this.onOpen,
  });

  final Message message;
  final VoidCallback onCopy;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final m = message;
    final sender = switch (m.auth) {
      'pass' => 'Sender verified',
      'fail' => 'Sender NOT verified',
      _ => 'Sender not checked',
    };
    return Card(
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(16),
        side: BorderSide(color: theme.colorScheme.primary, width: 2),
      ),
      child: InkWell(
        borderRadius: BorderRadius.circular(16),
        onTap: onOpen,
        child: Padding(
          padding: const EdgeInsets.all(20),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(m.title, style: theme.textTheme.titleMedium),
                        Text(
                          m.rcpt,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: theme.colorScheme.onSurfaceVariant,
                          ),
                        ),
                      ],
                    ),
                  ),
                  Text(
                    ago(m.receivedAt),
                    style: theme.textTheme.labelMedium?.copyWith(
                      color: theme.colorScheme.tertiary,
                    ),
                  ),
                ],
              ),
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 16),
                child: FittedBox(
                  fit: BoxFit.scaleDown,
                  child: SelectableText(
                    showCode(m.code),
                    style: codeStyle.copyWith(
                      fontSize: 44,
                      color: theme.colorScheme.onSurface,
                    ),
                  ),
                ),
              ),
              FilledButton(
                onPressed: onCopy,
                child: const Padding(
                  padding: EdgeInsets.symmetric(vertical: 14),
                  child: Text('Copy code'),
                ),
              ),
              const SizedBox(height: 8),
              Text(
                sender,
                textAlign: TextAlign.center,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _MessageTile extends StatelessWidget {
  const _MessageTile({
    required this.message,
    required this.now,
    required this.onTap,
    required this.onCopy,
    required this.onLink,
  });

  final Message message;
  final DateTime now;
  final VoidCallback onTap;
  final VoidCallback onCopy;
  final VoidCallback onLink;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final m = message;
    final muted = theme.colorScheme.onSurfaceVariant;
    final stale = m.used || !m.isFresh(now);
    return ListTile(
      onTap: onTap,
      title: Text(
        m.title,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
          fontWeight: FontWeight.w600,
          color: stale ? muted : null,
        ),
      ),
      subtitle: Text(
        '${m.subject.isEmpty ? '(no subject)' : m.subject}\n${ago(m.receivedAt, now: now)}${m.used ? ' · used' : ''}${m.unexpectedSender ? ' · unexpected sender' : ''}',
        maxLines: 2,
        overflow: TextOverflow.ellipsis,
      ),
      isThreeLine: true,
      trailing: m.hasCode
          ? TextButton(
              onPressed: onCopy,
              child: Text(
                showCode(m.code),
                style: codeStyle.copyWith(
                  fontSize: 17,
                  color: stale ? muted : theme.colorScheme.primary,
                ),
              ),
            )
          : m.hasLink
          ? TextButton(onPressed: onLink, child: const Text('Open link'))
          : null,
    );
  }
}

class AddressesPage extends StatefulWidget {
  const AddressesPage({super.key, required this.state});

  final AppState state;

  @override
  State<AddressesPage> createState() => _AddressesPageState();
}

class _AddressesPageState extends State<AddressesPage> {
  late Future<Addresses> _addresses = widget.state.api!.addresses();
  final _label = TextEditingController();

  @override
  void dispose() {
    _label.dispose();
    super.dispose();
  }

  void _reload() => setState(() => _addresses = widget.state.api!.addresses());

  Future<void> _copy(String address) async {
    await Clipboard.setData(ClipboardData(text: address));
    if (mounted) toast(context, 'Address copied');
  }

  Future<void> _generate() async {
    final label = _label.text.trim();
    if (label.isEmpty) return;
    await attempt(context, () async {
      final alias = await widget.state.api!.generateAlias(label);
      _label.clear();
      await _copy(alias.address);
      _reload();
    });
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = theme.colorScheme.onSurfaceVariant;
    return FutureBuilder<Addresses>(
      future: _addresses,
      builder: (context, snap) {
        if (snap.hasError) {
          return Center(
            child: Padding(
              padding: const EdgeInsets.all(24),
              child: Text('${snap.error}'),
            ),
          );
        }
        final data = snap.data;
        if (data == null) {
          return const Center(child: CircularProgressIndicator());
        }
        final builtIn = <(String, String)>[
          for (final d in data.sharedDomains) ...[
            if (d.allowMailbox) ('${data.username}@${d.name}', 'Your mailbox'),
            if (d.allowMailbox)
              ('${data.username}+anything@${d.name}', 'Add a tag per service'),
            if (d.allowSubdomains)
              (
                'anything@${data.username}.${d.name}',
                'For sites that reject "+"',
              ),
          ],
          for (final d in data.ownDomains) ('anything@$d', 'Your own domain'),
        ];
        return ListView(
          padding: const EdgeInsets.all(16),
          children: [
            Text('One address per service', style: theme.textTheme.titleMedium),
            const SizedBox(height: 4),
            Text(
              'Name the service and get an unguessable address just for it. You are warned if anyone else ever writes to it.',
              style: theme.textTheme.bodySmall?.copyWith(color: muted),
            ),
            const SizedBox(height: 12),
            Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _label,
                    textInputAction: TextInputAction.done,
                    onSubmitted: (_) => _generate(),
                    decoration: const InputDecoration(
                      labelText: 'Service name',
                      border: OutlineInputBorder(),
                      isDense: true,
                    ),
                  ),
                ),
                const SizedBox(width: 12),
                FilledButton(
                  onPressed: _generate,
                  child: const Text('Generate'),
                ),
              ],
            ),
            const SizedBox(height: 24),
            Text('Your addresses', style: theme.textTheme.titleMedium),
            const SizedBox(height: 8),
            if (data.aliases.isEmpty)
              Text(
                'You have not created any addresses yet.',
                style: TextStyle(color: muted),
              )
            else
              Card(
                margin: EdgeInsets.zero,
                child: Column(
                  children: [
                    for (final (i, a) in data.aliases.indexed) ...[
                      if (i > 0) const Divider(height: 1),
                      ListTile(
                        title: Text(
                          a.address,
                          style: codeStyle.copyWith(
                            fontSize: 14,
                            letterSpacing: 0,
                          ),
                        ),
                        subtitle: Text(
                          [
                            if (a.label.isNotEmpty) a.label,
                            if (a.generated)
                              a.expectedSender.isEmpty
                                  ? 'no mail yet'
                                  : 'expects ${a.expectedSender}',
                          ].join(' · '),
                        ),
                        onTap: () => _copy(a.address),
                        trailing: IconButton(
                          icon: const Icon(Icons.delete_outline),
                          tooltip: 'Release ${a.address}',
                          onPressed: () => attempt(context, () async {
                            await widget.state.api!.deleteAlias(a.id);
                            _reload();
                          }),
                        ),
                      ),
                    ],
                  ],
                ),
              ),
            const SizedBox(height: 24),
            Text('Always yours', style: theme.textTheme.titleMedium),
            const SizedBox(height: 8),
            Card(
              margin: EdgeInsets.zero,
              child: Column(
                children: [
                  for (final (i, (address, note)) in builtIn.indexed) ...[
                    if (i > 0) const Divider(height: 1),
                    ListTile(
                      dense: true,
                      title: Text(
                        address,
                        style: codeStyle.copyWith(
                          fontSize: 14,
                          letterSpacing: 0,
                        ),
                      ),
                      subtitle: Text(note),
                      onTap: address.contains('anything')
                          ? null
                          : () => _copy(address),
                    ),
                  ],
                ],
              ),
            ),
            const SizedBox(height: 16),
            Text(
              'Tap an address to copy it. Claim custom addresses and manage domains in the web app.',
              style: theme.textTheme.bodySmall?.copyWith(color: muted),
            ),
          ],
        );
      },
    );
  }
}

class SettingsPage extends StatelessWidget {
  const SettingsPage({super.key, required this.state});

  final AppState state;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final host = Uri.tryParse(state.server ?? '')?.host ?? '';
    return ListView(
      children: [
        ListTile(
          leading: const Icon(Icons.dns_outlined),
          title: Text(host),
          subtitle: Text(
            state.username == null
                ? 'Connected'
                : 'Signed in as ${state.username}',
          ),
        ),
        const Divider(),
        SwitchListTile(
          title: const Text('Copy new codes to the clipboard'),
          subtitle: const Text('As soon as a code arrives'),
          value: state.autoCopy,
          onChanged: state.setAutoCopy,
        ),
        ListTile(
          title: const Text('Clear the clipboard after'),
          subtitle: const Text('Only if it still holds the code'),
          trailing: DropdownButton<int>(
            value: state.clearClipboardSeconds,
            onChanged: (v) => state.setClearClipboardSeconds(v ?? 60),
            items: const [
              DropdownMenuItem(value: 30, child: Text('30 seconds')),
              DropdownMenuItem(value: 60, child: Text('60 seconds')),
              DropdownMenuItem(value: 300, child: Text('5 minutes')),
              DropdownMenuItem(value: 0, child: Text('Never')),
            ],
          ),
        ),
        SwitchListTile(
          title: const Text('Show the code in notifications'),
          value: state.showCodeInNotifications,
          onChanged: state.setShowCodeInNotifications,
        ),
        if (isMobile && Platform.isAndroid)
          SwitchListTile(
            title: const Text('Stay connected in the background'),
            subtitle: const Text(
              'Shows a permanent notification. Without it, codes arrive only while the app is open.',
            ),
            value: state.stayConnected,
            onChanged: state.setStayConnected,
          ),
        const Divider(),
        ListTile(
          leading: const Icon(Icons.open_in_browser),
          title: const Text('Manage your account in the browser'),
          subtitle: const Text('Passkeys, addresses, domains and devices'),
          onTap: () => launchUrl(
            Uri.parse(state.server!),
            mode: LaunchMode.externalApplication,
          ),
        ),
        ListTile(
          leading: Icon(Icons.logout, color: theme.colorScheme.error),
          title: Text(
            'Disconnect this device',
            style: TextStyle(color: theme.colorScheme.error),
          ),
          onTap: state.signOut,
        ),
      ],
    );
  }
}
