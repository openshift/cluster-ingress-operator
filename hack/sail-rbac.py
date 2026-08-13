#!/usr/bin/env python3
"""
Generate and verify the istiod runtime RBAC rules in the Sail Library ClusterRole.

Usage:
  sail-rbac.py generate <manifest> <resources-dir>
  sail-rbac.py verify   <manifest> <clusterrole-template> <role-template>
"""
import re
import sys
from collections import defaultdict
from pathlib import Path

# Conditional blocks in the istiod Helm chart not active in CIO's install path.
# Rules inside these blocks are excluded from the generated manifest.
SKIP_CONDITIONS = [
    'enableAnalysis',
    'taint.enabled',
    'EXTERNAL_CA',
    'ENABLE_CLUSTER_TRUST_BUNDLE_API',
    'pilotCertProvider',
    'istiodRemote.enabled',
]

# Go template directives that open a new scope, all balanced with {{- end }}.
# range/with/block must be counted alongside if so their end does not prematurely
# clear a skip_depth set by an enclosing if.
SCOPE_OPEN = re.compile(r'\{\{-?\s*(if|range|with|block)\b')
SCOPE_CLOSE = re.compile(r'\{\{-?\s*end\b')

# The outer {{- if}} blocks in clusterrole.yaml always evaluate true for CIO.
OUTER_DEPTH = 1

MCS_API_GROUP = 'multicluster.x-k8s.io'
GENERATED_MARKER = '# --- BEGIN GENERATED: do not edit below; run hack/update-sail-rbac.sh to regenerate ---'

NON_RULE_KEYS = {
    'kind', 'apiVersion', 'metadata', 'name', 'namespace',
    'labels', 'release', 'app', 'rules', 'app.kubernetes.io/name',
}


# ── Parsing ───────────────────────────────────────────────────────────────────

def parse_inline_list(s):
    """Parse ["a", "b"] or a bare scalar into a list of strings."""
    s = s.strip()
    if not s.startswith('['):
        val = s.strip('"\'').strip()
        return [val] if val else []
    content = s[1:s.rfind(']')]
    return [p.strip().strip('"\'') for p in content.split(',') if p.strip().strip('"\'')]


def parse_rules(text):
    """
    Parse RBAC rules from YAML text without external dependencies.
    Handles both inline format (["a", "b"]) and block format (- a\\n- b).
    """
    rules = []
    current = None
    key = None

    for line in text.splitlines():
        s = line.strip()
        if not s or s.startswith('#') or s == '---':
            continue

        colon_key = s.split(':')[0].strip().lstrip('- ')
        if colon_key in NON_RULE_KEYS:
            current = None
            key = None
            continue

        if 'apiGroups:' in s:
            if current and (current['apiGroups'] or current['resources'] or current['verbs']):
                rules.append(current)
            current = {'apiGroups': [], 'resources': [], 'verbs': []}
            key = 'apiGroups'
            rest = s.split('apiGroups:', 1)[1].strip()
            if rest:
                current['apiGroups'] = parse_inline_list(rest)

        elif current is not None and 'resources:' in s and ':' in s and not s.startswith('- '):
            key = 'resources'
            rest = s.split('resources:', 1)[1].strip()
            if rest:
                current['resources'] = parse_inline_list(rest)

        elif current is not None and 'verbs:' in s and ':' in s and not s.startswith('- '):
            key = 'verbs'
            rest = s.split('verbs:', 1)[1].strip()
            if rest:
                current['verbs'] = parse_inline_list(rest)

        elif current is not None and key is not None and s.startswith('- '):
            item = s[2:].strip().strip('"\'')
            if ':' not in item:  # skip resourceNames and other sub-keys
                current[key].append(item)

    if current and (current['apiGroups'] or current['resources'] or current['verbs']):
        rules.append(current)

    return rules


def parse_template(path):
    """Extract unconditional YAML from a Go template file."""
    lines = Path(path).read_text().splitlines()
    result = []
    depth = 0
    skip_depth = None

    for line in lines:
        s = line.strip()

        if SCOPE_OPEN.match(s):
            depth += 1
            if skip_depth is None and depth > OUTER_DEPTH and re.match(r'\{\{-?\s*if\b', s):
                if any(cond in s for cond in SKIP_CONDITIONS):
                    skip_depth = depth
        elif SCOPE_CLOSE.match(s):
            if skip_depth is not None and depth == skip_depth:
                skip_depth = None
            depth -= 1

        if s.startswith('{{'):
            continue
        if skip_depth is not None:
            continue
        if depth < OUTER_DEPTH:
            continue

        line = line.replace('{{ $mcsAPIGroup }}', MCS_API_GROUP)
        if '{{' in line:
            continue

        result.append(line)

    return '\n'.join(result)


# ── Generate ──────────────────────────────────────────────────────────────────

def merge_rules(all_rules):
    """Merge rules across versions: group by (apiGroups, resources), union verbs."""
    merged = defaultdict(set)
    for rule in all_rules:
        groups = tuple(sorted(rule.get('apiGroups') or []))
        resources = tuple(sorted(rule.get('resources') or []))
        verbs = rule.get('verbs') or []
        if groups and resources and verbs:
            merged[(groups, resources)].update(verbs)

    # Remove entries that are strict subsets of another entry with identical verbs
    keys = list(merged.keys())
    redundant = set()
    for i, (g1, r1) in enumerate(keys):
        if (g1, r1) in redundant:
            continue
        for j, (g2, r2) in enumerate(keys):
            if i == j or (g2, r2) in redundant:
                continue
            if g1 == g2 and set(r2).issubset(set(r1)) and merged[(g1, r1)] == merged[(g2, r2)]:
                redundant.add((g2, r2))
    for k in redundant:
        del merged[k]

    return merged


def yaml_scalar(s):
    """Quote YAML scalars that would otherwise be interpreted as anchors/aliases."""
    return f"'{s}'" if s in ('*', '') else s


def format_rules(merged):
    """Format merged rules as YAML rule blocks."""
    lines = []
    for (groups, resources), verbs in sorted(merged.items()):
        lines.append('- apiGroups:')
        for g in groups:
            lines.append(f'  - {yaml_scalar(g)}')
        lines.append('  resources:')
        for r in resources:
            lines.append(f'  - {yaml_scalar(r)}')
        lines.append('  verbs:')
        for v in sorted(verbs):
            lines.append(f'  - {yaml_scalar(v)}')
        lines.append('')
    return '\n'.join(lines)


def cmd_generate(manifest_path, resources_dir):
    resources_dir = Path(resources_dir)
    versions = sorted([d for d in resources_dir.iterdir() if d.is_dir()], key=lambda p: p.name)
    if not versions:
        print(f'ERROR: no version directories found in {resources_dir}', file=sys.stderr)
        sys.exit(1)

    print(f'  Versions: {", ".join(v.name for v in versions)}')

    all_rules = []
    for vdir in versions:
        for tpl in ['charts/istiod/templates/clusterrole.yaml',
                    'charts/istiod/templates/reader-clusterrole.yaml',
                    'charts/istiod/templates/role.yaml']:
            path = vdir / tpl
            if path.exists():
                all_rules.extend(parse_rules(parse_template(str(path))))

    merged = merge_rules(all_rules)
    generated = format_rules(merged)

    manifest_text = Path(manifest_path).read_text()
    if GENERATED_MARKER not in manifest_text:
        print(f'ERROR: marker not found in {manifest_path}', file=sys.stderr)
        sys.exit(1)

    static = manifest_text[:manifest_text.index(GENERATED_MARKER) + len(GENERATED_MARKER)]
    Path(manifest_path).write_text(static + '\n\n' + generated)
    print(f'  Written {len(merged)} rules to {manifest_path}')


# ── Verify ────────────────────────────────────────────────────────────────────

def covers(manifest_rules, api_group, resource, verb):
    for rule in manifest_rules:
        if (api_group in (rule.get('apiGroups') or []) or '*' in (rule.get('apiGroups') or [])) and \
           (resource in (rule.get('resources') or []) or '*' in (rule.get('resources') or [])) and \
           (verb in (rule.get('verbs') or []) or '*' in (rule.get('verbs') or [])):
            return True
    return False


def cmd_verify(manifest_path, *template_paths):
    manifest_rules = parse_rules(Path(manifest_path).read_text())

    chart_rules = []
    for path in template_paths:
        chart_rules.extend(parse_rules(parse_template(path)))

    missing = set()
    for rule in chart_rules:
        for group in (rule.get('apiGroups') or []):
            for resource in (rule.get('resources') or []):
                for verb in (rule.get('verbs') or []):
                    if not covers(manifest_rules, group, resource, verb):
                        missing.add(f'  {group or "core"}/{resource}: {verb}')

    if missing:
        print('ERROR: manifests/00-cluster-role-sail-library.yaml is missing the following')
        print('permissions required by istiod. Run hack/update-sail-rbac.sh to regenerate.')
        print()
        for m in sorted(missing):
            print(m)
        sys.exit(1)

    print('OK')


# ── Entry point ───────────────────────────────────────────────────────────────

def main():
    if len(sys.argv) < 2 or sys.argv[1] not in ('generate', 'verify'):
        print(__doc__, file=sys.stderr)
        sys.exit(1)

    cmd = sys.argv[1]
    args = sys.argv[2:]

    if cmd == 'generate':
        if len(args) != 2:
            print('Usage: sail-rbac.py generate <manifest> <resources-dir>', file=sys.stderr)
            sys.exit(1)
        cmd_generate(*args)
    else:
        if len(args) < 2:
            print('Usage: sail-rbac.py verify <manifest> <template>...', file=sys.stderr)
            sys.exit(1)
        cmd_verify(*args)


main()
