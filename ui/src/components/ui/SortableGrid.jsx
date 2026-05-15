import React from 'react';
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from '@dnd-kit/core';
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  rectSortingStrategy,
  useSortable,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { GripVertical } from 'lucide-react';

export const GripHandle = React.forwardRef((props, ref) => (
  <button
    ref={ref}
    {...props}
    aria-label="拖拽排序"
    type="button"
    className="cursor-grab active:cursor-grabbing text-on-surface-variant hover:text-on-surface p-1 rounded-control hover:bg-on-surface/[0.04]"
  >
    <GripVertical size={14} />
  </button>
));

GripHandle.displayName = 'GripHandle';

function SortableItem({ id, item, renderItem }) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    ...(isDragging ? { opacity: 0.5, zIndex: 10 } : {}),
  };

  if (isDragging) {
    if (style.transform) {
      style.transform += ' scale(1.05)';
    } else {
      style.transform = 'scale(1.05)';
    }
  }

  return (
    <div ref={setNodeRef} style={style} className={isDragging ? 'cursor-grabbing' : ''}>
      {renderItem(item, { ...attributes, ...listeners })}
    </div>
  );
}

const SortableGrid = ({
  items,
  getId,
  onReorder,
  renderItem,
  columns = 3,
  gap = 'gap-3',
  className = '',
}) => {
  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: {
        distance: 5,
      },
    }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  const handleDragEnd = (event) => {
    const { active, over } = event;
    if (over && active.id !== over.id) {
      const oldIndex = items.findIndex((i) => getId(i) === active.id);
      const newIndex = items.findIndex((i) => getId(i) === over.id);
      const newItems = arrayMove(items, oldIndex, newIndex);
      onReorder(newItems.map(getId), newItems);
    }
  };

  const itemIds = items.map(getId);

  let gridColsClass = 'grid-cols-1 md:grid-cols-2 xl:grid-cols-3';
  if (columns === 2) gridColsClass = 'grid-cols-1 md:grid-cols-2';
  else if (columns === 1) gridColsClass = 'grid-cols-1';

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragEnd={handleDragEnd}
    >
      <SortableContext
        items={itemIds}
        strategy={rectSortingStrategy}
      >
        <div 
          className={`grid ${gridColsClass} ${gap} ${className}`}
          aria-describedby="sortable-grid-instructions"
        >
          {items.map((item) => {
            const id = getId(item);
            return (
              <SortableItem
                key={id}
                id={id}
                item={item}
                renderItem={renderItem}
              />
            );
          })}
        </div>
        <div id="sortable-grid-instructions" className="sr-only">
          使用 Tab 键选中拖拽手柄，按下空格键或回车键拾取。使用方向键移动，再次按下空格键或回车键放下，或按下 Esc 键取消。
        </div>
      </SortableContext>
    </DndContext>
  );
};

export default SortableGrid;
