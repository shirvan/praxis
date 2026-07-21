import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import type { PraxisResource } from './resources';

export interface SchemaField {
  name: string;
  required: boolean;
  type: string;
  constraint: string;
  defaultValue?: string;
  description: string;
}

export interface ResourceExample {
  label: string;
  path: string;
  url: string;
}

export interface ResourceReference {
  schemaSource: string;
  specFields: SchemaField[];
  outputFields: SchemaField[];
  example: string;
  exampleOrigin: 'curated' | 'repository' | 'generated';
  exampleSource?: ResourceExample;
  examples: ResourceExample[];
}

const repoRoot = existsSync(join(process.cwd(), 'schemas')) ? process.cwd() : resolve(process.cwd(), '..');
const githubRoot = 'https://github.com/shirvan/praxis/blob/main/';

function walkCueFiles(directory: string): string[] {
  if (!existsSync(directory)) return [];
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return walkCueFiles(path);
    return entry.isFile() && entry.name.endsWith('.cue') ? [path] : [];
  });
}

const exampleFiles = walkCueFiles(join(repoRoot, 'examples'));

function findClosingDelimiter(source: string, openingIndex: number, opening = '{', closing = '}'): number {
  let depth = 0;
  let quote = '';
  let escaped = false;
  let lineComment = false;

  for (let index = openingIndex; index < source.length; index += 1) {
    const character = source[index];
    const next = source[index + 1];

    if (lineComment) {
      if (character === '\n') lineComment = false;
      continue;
    }
    if (quote) {
      if (escaped) escaped = false;
      else if (character === '\\') escaped = true;
      else if (character === quote) quote = '';
      continue;
    }
    if (character === '/' && next === '/') {
      lineComment = true;
      index += 1;
      continue;
    }
    if (character === '"' || character === "'") {
      quote = character;
      continue;
    }
    if (character === opening) depth += 1;
    if (character === closing) {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function extractObject(source: string, field: string): string {
  const match = new RegExp(`(?:^|\\n)\\s*${field}\\??\\s*:\\s*\\{`, 'm').exec(source);
  if (!match) return '';
  const openingIndex = source.indexOf('{', match.index);
  const closingIndex = findClosingDelimiter(source, openingIndex);
  return closingIndex === -1 ? '' : source.slice(openingIndex + 1, closingIndex);
}

function delimiterDelta(line: string): number {
  let delta = 0;
  let quote = '';
  let escaped = false;
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index];
    if (!quote && character === '/' && line[index + 1] === '/') break;
    if (quote) {
      if (escaped) escaped = false;
      else if (character === '\\') escaped = true;
      else if (character === quote) quote = '';
      continue;
    }
    if (character === '"' || character === "'") quote = character;
    else if ('{[('.includes(character)) delta += 1;
    else if ('}])'.includes(character)) delta -= 1;
  }
  return delta;
}

function humanizeField(name: string): string {
  const words = name
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/Ids\b/g, 'IDs')
    .replace(/Id\b/g, 'ID')
    .replace(/Arn\b/g, 'ARN')
    .replace(/Acls\b/g, 'ACLs')
    .replace(/Vpc\b/g, 'VPC')
    .replace(/Dns\b/g, 'DNS')
    .replace(/Ip\b/g, 'IP')
    .replace(/Url\b/g, 'URL')
    .replace(/Sse\b/g, 'SSE');
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}.`;
}

function summarizeType(constraint: string): string {
  const value = constraint.trim();
  if (value.startsWith('[string]:') || value.includes('[string]:')) return 'Map';
  if (value.startsWith('[...')) return value.includes('{') ? 'Object list' : 'List';
  if (value.startsWith('{') || /^\w+\??\s*:/.test(value)) return 'Object';
  if (/^#\w+/.test(value)) return 'Schema reference';
  if ((value.match(/"/g)?.length ?? 0) >= 2 && value.includes('|')) return 'Enum';
  if (/\bbool\b/.test(value)) return 'Boolean';
  if (/\bint\b/.test(value) || /^\d+(\s*\||$)/.test(value)) return 'Integer';
  if (/\bnumber\b/.test(value)) return 'Number';
  if (/\bbytes\b/.test(value)) return 'Bytes';
  if (/\bstring\b/.test(value) || value.startsWith('"')) return 'String';
  return 'CUE value';
}

function extractDefault(constraint: string): string | undefined {
  let depth = 0;
  let quote = '';
  let escaped = false;
  for (let index = 0; index < constraint.length; index += 1) {
    const character = constraint[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (character === '\\') escaped = true;
      else if (character === quote) quote = '';
      continue;
    }
    if (character === '"' || character === "'") quote = character;
    else if ('{[('.includes(character)) depth += 1;
    else if ('}])'.includes(character)) depth -= 1;
    else if (character === '*' && depth === 0) {
      let end = index + 1;
      let valueDepth = 0;
      let valueQuote = '';
      let valueEscaped = false;
      for (; end < constraint.length; end += 1) {
        const valueCharacter = constraint[end];
        if (valueQuote) {
          if (valueEscaped) valueEscaped = false;
          else if (valueCharacter === '\\') valueEscaped = true;
          else if (valueCharacter === valueQuote) valueQuote = '';
          continue;
        }
        if (valueCharacter === '"' || valueCharacter === "'") valueQuote = valueCharacter;
        else if ('{[('.includes(valueCharacter)) valueDepth += 1;
        else if ('}])'.includes(valueCharacter)) valueDepth -= 1;
        else if (valueCharacter === '|' && valueDepth === 0) break;
      }
      const value = constraint.slice(index + 1, end).trim().replace(/,$/, '');
      return value.length > 54 ? `${value.slice(0, 51)}…` : value;
    }
  }
  return undefined;
}

function parseFields(block: string): SchemaField[] {
  const fields: SchemaField[] = [];
  const lines = block.split('\n');
  let comments: string[] = [];
  let fieldLines: string[] = [];
  let fieldName = '';
  let optional = false;
  let depth = 0;

  const finishField = () => {
    if (!fieldName) return;
    const declaration = fieldLines.join('\n').trim();
    const colon = declaration.indexOf(':');
    const constraint = declaration.slice(colon + 1)
      .replace(/\s*\/\/.*$/gm, '')
      .replace(/\s+/g, ' ')
      .trim();
    const defaultValue = extractDefault(constraint);
    fields.push({
      name: fieldName,
      required: !optional && defaultValue === undefined,
      type: summarizeType(constraint),
      constraint,
      defaultValue,
      description: comments.length ? comments.join(' ') : humanizeField(fieldName),
    });
    comments = [];
    fieldLines = [];
    fieldName = '';
    optional = false;
    depth = 0;
  };

  for (const line of lines) {
    const trimmed = line.trim();
    if (!fieldName && trimmed.startsWith('//')) {
      comments.push(trimmed.replace(/^\/\/\s?/, ''));
      continue;
    }
    if (!fieldName && !trimmed) {
      comments = [];
      continue;
    }
    if (!fieldName) {
      const match = /^([A-Za-z][A-Za-z0-9_]*)\s*(\?)?\s*:/.exec(trimmed);
      if (!match) {
        comments = [];
        continue;
      }
      fieldName = match[1];
      optional = Boolean(match[2]);
      fieldLines = [trimmed];
      depth = delimiterDelta(trimmed.slice(trimmed.indexOf(':') + 1));
      if (depth <= 0) finishField();
      continue;
    }

    fieldLines.push(trimmed);
    depth += delimiterDelta(trimmed);
    if (depth <= 0) finishField();
  }
  finishField();
  return fields;
}

const valueByField: Record<string, string> = {
  region: '"us-west-2"',
  account: '"production"',
  cidrBlock: '"10.42.0.0/16"',
  ipv6CidrBlock: '"2001:db8:1234::/64"',
  availabilityZone: '"us-west-2a"',
  imageId: '"ami-0123456789abcdef0"',
  instanceType: '"t3.small"',
  instanceClass: '"db.t3.small"',
  engine: '"postgres"',
  engineVersion: '"16.3"',
  hashKey: '"id"',
  hashKeyType: '"S"',
  groupName: '"application"',
  description: '"Managed by Praxis"',
  domainName: '"api.example.com"',
  hostedZoneId: '"Z0123456789ABCDEF"',
  roleArn: '"arn:aws:iam::123456789012:role/praxis-resource"',
  role: '"arn:aws:iam::123456789012:role/praxis-resource"',
  assumeRolePolicyDocument: '"{\\"Version\\":\\"2012-10-17\\",\\"Statement\\":[]}"',
  policyDocument: '"{\\"Version\\":\\"2012-10-17\\",\\"Statement\\":[]}"',
  dashboardBody: '"{\\"widgets\\":[]}"',
  value: '"replace-with-a-secure-value"',
  tags: '{environment: "dev"}',
  inlinePolicies: '{}',
  parameters: '{}',
  managedPolicyArns: '[]',
  capacityProviders: '["FARGATE"]',
  subnetIds: '["subnet-0123456789abcdef0", "subnet-0fedcba9876543210"]',
  securityGroupIds: '["sg-0123456789abcdef0"]',
  vpcSecurityGroupIds: '["sg-0123456789abcdef0"]',
  vpcId: '"${resources.network.outputs.vpcId}"',
  subnetId: '"${resources.subnet.outputs.subnetId}"',
  allocationId: '"${resources.address.outputs.allocationId}"',
  repositoryName: '"application"',
  functionName: '"processor"',
  functionArn: '"arn:aws:lambda:us-west-2:123456789012:function:processor"',
  topicArn: '"arn:aws:sns:us-west-2:123456789012:events"',
  queueUrl: '"https://sqs.us-west-2.amazonaws.com/123456789012/events"',
};

function exampleValue(field: SchemaField): string {
  if (valueByField[field.name]) return valueByField[field.name];
  if (field.defaultValue) return field.defaultValue;
  const quoted = field.constraint.match(/"([^"\\]*(?:\\.[^"\\]*)*)"/);
  if (field.type === 'Enum' && quoted) return `"${quoted[1]}"`;
  if (field.type === 'String') return `"replace-${field.name}"`;
  if (field.type === 'Boolean') return 'true';
  if (field.type === 'Integer' || field.type === 'Number') {
    const minimum = field.constraint.match(/>=?\s*(\d+)/)?.[1];
    return minimum ?? '1';
  }
  if (field.type === 'List' || field.type === 'Object list') return '[]';
  if (field.type === 'Map') return '{}';
  if (field.name === 'source') return '{fromAMI: {sourceImageId: "ami-0123456789abcdef0"}}';
  if (field.name === 'code') return '{s3: {bucket: "release-artifacts", key: "application.zip"}}';
  return '{}';
}

function exampleName(kind: string): string {
  return kind.charAt(0).toLowerCase() + kind.slice(1).replace(/([a-z])([A-Z])/g, '$1$2');
}

function generateExample(resource: PraxisResource, specFields: SchemaField[]): string {
  const requiredFields = specFields.filter((field) => field.required);
  const lines = requiredFields.map((field) => `    ${field.name}: ${exampleValue(field)}`);
  return `resources: ${exampleName(resource.kind)}: {
  apiVersion: "praxis.io/alpha"
  kind:       "${resource.kind}"
  metadata: {
    name:   "example-${resource.slug}"
    labels: {environment: "dev"}
  }
  spec: {
${lines.join('\n')}
  }
}`;
}

function findExamples(kind: string): ResourceExample[] {
  const kindPattern = new RegExp(`kind\\s*:\\s*"${kind}"`);
  return exampleFiles.flatMap((absolutePath) => {
    const source = readFileSync(absolutePath, 'utf8');
    if (!kindPattern.test(source)) return [];
    const relativePath = absolutePath.slice(repoRoot.length + 1);
    return [{
      label: relativePath.replace(/^examples\//, '').replace(/\.cue$/, '').replace(/[-_/]/g, ' '),
      path: relativePath,
      url: `${githubRoot}${relativePath}`,
    }];
  });
}

function findContainingObject(source: string, targetIndex: number): number {
  const stack: number[] = [];
  let quote = '';
  let escaped = false;
  let lineComment = false;
  for (let index = 0; index < targetIndex; index += 1) {
    const character = source[index];
    const next = source[index + 1];
    if (lineComment) {
      if (character === '\n') lineComment = false;
      continue;
    }
    if (quote) {
      if (escaped) escaped = false;
      else if (character === '\\') escaped = true;
      else if (character === quote) quote = '';
      continue;
    }
    if (character === '/' && next === '/') {
      lineComment = true;
      index += 1;
    } else if (character === '"' || character === "'") quote = character;
    else if (character === '{') stack.push(index);
    else if (character === '}') stack.pop();
  }
  return stack.at(-1) ?? -1;
}

function indentLines(source: string, spaces: number): string {
  const prefix = ' '.repeat(spaces);
  return source.split('\n').map((line) => line ? `${prefix}${line}` : line).join('\n');
}

function extractExampleResource(source: string, kind: string): string | undefined {
  const kindIndex = source.search(new RegExp(`kind\\s*:\\s*"${kind}"`));
  if (kindIndex === -1) return undefined;
  const openingIndex = findContainingObject(source, kindIndex);
  if (openingIndex === -1) return undefined;
  const closingIndex = findClosingDelimiter(source, openingIndex);
  if (closingIndex === -1) return undefined;

  const lineStart = source.lastIndexOf('\n', openingIndex) + 1;
  const rawSnippet = source.slice(lineStart, closingIndex + 1).trimEnd();
  const leadingWhitespace = rawSnippet.match(/^\s*/)?.[0] ?? '';
  const snippet = rawSnippet.split('\n').map((line) => line.startsWith(leadingWhitespace)
    ? line.slice(leadingWhitespace.length)
    : line).join('\n');

  return snippet.trimStart().startsWith('resources:')
    ? snippet
    : `resources: {\n${indentLines(snippet, 2)}\n}`;
}

export function loadResourceReference(resource: PraxisResource): ResourceReference {
  const schemaPath = join(repoRoot, resource.schema);
  const schemaSource = readFileSync(schemaPath, 'utf8').trim();
  const specFields = parseFields(extractObject(schemaSource, 'spec'));
  const outputFields = parseFields(extractObject(schemaSource, 'outputs'));
  const examples = findExamples(resource.kind);
  const exampleSource = examples[0];
  const repositoryExample = exampleSource
    ? extractExampleResource(readFileSync(join(repoRoot, exampleSource.path), 'utf8'), resource.kind)
    : undefined;
  const exampleOrigin = resource.details?.example ? 'curated' : repositoryExample ? 'repository' : 'generated';
  const example = resource.details?.example ?? repositoryExample ?? generateExample(resource, specFields);
  if (specFields.length === 0 || outputFields.length === 0) {
    throw new Error(`Unable to derive the public field reference for ${resource.kind} from ${resource.schema}`);
  }
  if (!example.includes(`kind:`) || !example.includes(`"${resource.kind}"`)) {
    throw new Error(`Unable to derive a representative ${resource.kind} example`);
  }
  return {
    schemaSource,
    specFields,
    outputFields,
    example,
    exampleOrigin,
    exampleSource: exampleOrigin === 'repository' ? exampleSource : undefined,
    examples,
  };
}
