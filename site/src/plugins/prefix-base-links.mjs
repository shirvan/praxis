export default function prefixBaseLinks({ base = '/' } = {}) {
  const prefix = `${base.replace(/^\/+|\/+$/g, '')}/`;
  if (prefix === '/') return;

  return (tree) => {
    const visit = (node) => {
      if (node?.type === 'element' && node.properties) {
        for (const property of ['href', 'src']) {
          const value = node.properties[property];
          if (typeof value === 'string' && value.startsWith('/') && !value.startsWith('//')) {
            node.properties[property] = `/${prefix}${value.slice(1)}`;
          }
        }
      }
      node?.children?.forEach(visit);
    };

    visit(tree);
  };
}
