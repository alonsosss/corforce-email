export interface TabItem<K extends string> {
  id: K;
  label: string;
}

export interface TabsProps<K extends string> {
  items: readonly TabItem<K>[];
  value: K;
  onChange: (id: K) => void;
  label: string;
}

export function Tabs<K extends string>({ items, value, onChange, label }: TabsProps<K>) {
  return (
    <div className="cf-tabs" role="tablist" aria-label={label}>
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          role="tab"
          className="cf-tab"
          aria-selected={item.id === value}
          onClick={() => onChange(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}
