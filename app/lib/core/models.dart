// Data returned by the Codemail API.

class Message {
  Message.fromJson(Map<String, dynamic> j)
    : id = j['id'] as int,
      kind = j['kind'] as String? ?? 'other',
      rcpt = j['rcpt'] as String? ?? '',
      from = j['from'] as String? ?? '',
      subject = j['subject'] as String? ?? '',
      service = j['service'] as String? ?? '',
      code = j['code'] as String? ?? '',
      link = j['link'] as String? ?? '',
      linkHost = j['link_host'] as String? ?? '',
      linkMatchesSender = j['link_matches_sender'] as bool? ?? false,
      confidence = (j['confidence'] as num?)?.toDouble() ?? 0,
      auth = j['auth'] as String? ?? 'none',
      authDetail = j['auth_detail'] as String? ?? '',
      senderDomain = j['sender_domain'] as String? ?? '',
      flags = (j['flags'] as List?)?.cast<String>() ?? const [],
      receivedAt = _time(j['received_at']),
      expiresAt = _time(j['expires_at']),
      usedAt = _time(j['used_at']),
      markdown = j['markdown'] as String?;

  final int id;
  final String kind; // "code" (has a code or link) or "other"
  final String rcpt;
  final String from;
  final String subject;
  final String service;
  final String code;
  final String link;
  final String linkHost;
  final bool linkMatchesSender;
  final double confidence;
  final String auth; // "pass", "fail" or "none"
  final String authDetail;
  final String senderDomain;
  final List<String> flags;
  final DateTime? receivedAt;
  final DateTime? expiresAt;
  final DateTime? usedAt;
  final String? markdown; // only on a single fetched message

  bool get hasCode => code.isNotEmpty;
  bool get hasLink => link.isNotEmpty;
  bool get used => usedAt != null;
  bool get unexpectedSender => flags.contains('unexpected_sender');
  String get title => service.isNotEmpty
      ? service
      : (from.isNotEmpty ? from : 'Unknown sender');

  /// A code is shown as current for ten minutes, unless it has been used.
  bool isFresh(DateTime now) =>
      hasCode &&
      !used &&
      receivedAt != null &&
      now.difference(receivedAt!) < const Duration(minutes: 10);

  static DateTime? _time(Object? seconds) => seconds is int && seconds > 0
      ? DateTime.fromMillisecondsSinceEpoch(seconds * 1000)
      : null;
}

class Alias {
  Alias.fromJson(Map<String, dynamic> j)
    : id = j['id'] as int,
      address = j['address'] as String,
      label = j['label'] as String? ?? '',
      generated = j['generated'] as bool? ?? false,
      expectedSender = j['expected_sender'] as String? ?? '';

  final int id;
  final String address;
  final String label;
  final bool generated;
  final String expectedSender;
}

class SharedDomain {
  SharedDomain.fromJson(Map<String, dynamic> j)
    : id = j['id'] as int,
      name = j['name'] as String,
      allowMailbox = j['allow_mailbox'] as bool? ?? false,
      allowAliases = j['allow_aliases'] as bool? ?? false,
      allowSubdomains = j['allow_subdomains'] as bool? ?? false;

  final int id;
  final String name;
  final bool allowMailbox;
  final bool allowAliases;
  final bool allowSubdomains;
}

class Addresses {
  Addresses.fromJson(Map<String, dynamic> j)
    : username = j['username'] as String,
      sharedDomains = [
        for (final d in j['shared_domains'] as List)
          SharedDomain.fromJson(d as Map<String, dynamic>),
      ],
      ownDomains = [
        for (final d in j['domains'] as List)
          if ((d as Map<String, dynamic>)['verified'] == true)
            d['name'] as String,
      ],
      aliases = [
        for (final a in j['aliases'] as List)
          Alias.fromJson(a as Map<String, dynamic>),
      ];

  final String username;
  final List<SharedDomain> sharedDomains;
  final List<String> ownDomains;
  final List<Alias> aliases;
}

class Tokens {
  const Tokens({
    required this.access,
    required this.refresh,
    required this.expiresAt,
  });

  factory Tokens.fromJson(Map<String, dynamic> j, {DateTime? now}) => Tokens(
    access: j['access_token'] as String,
    refresh: j['refresh_token'] as String,
    expiresAt: (now ?? DateTime.now()).add(
      Duration(seconds: (j['expires_in'] as num?)?.toInt() ?? 3600),
    ),
  );

  final String access;
  final String refresh;
  final DateTime expiresAt;
}
